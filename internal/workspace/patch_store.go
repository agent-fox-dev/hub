package workspace

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Patch represents a row in the patches table (15-REQ-7).
type Patch struct {
	ID            string   // TEXT PRIMARY KEY (UUID)
	WorkspaceSlug string   // TEXT NOT NULL
	BranchName    string   // TEXT NOT NULL
	Position      int      // INTEGER NOT NULL
	Status        string   // TEXT NOT NULL DEFAULT 'active'
	ConflictFiles []string // TEXT (nullable, JSON array)
	UpstreamPRURL *string  // TEXT (nullable)
	Description   *string  // TEXT (nullable)
	AddedAt       string   // TEXT NOT NULL (RFC 3339)
	UpdatedAt     string   // TEXT NOT NULL (RFC 3339)
}

// patchResponse converts a Patch to a JSON-serializable map for API responses.
func patchResponse(p *Patch) map[string]any {
	resp := map[string]any{
		"id":             p.ID,
		"workspace_slug": p.WorkspaceSlug,
		"branch_name":    p.BranchName,
		"position":       p.Position,
		"status":         p.Status,
		"added_at":       p.AddedAt,
		"updated_at":     p.UpdatedAt,
	}
	if len(p.ConflictFiles) > 0 {
		resp["conflict_files"] = p.ConflictFiles
	}
	if p.UpstreamPRURL != nil {
		resp["upstream_pr_url"] = *p.UpstreamPRURL
	} else {
		resp["upstream_pr_url"] = nil
	}
	if p.Description != nil {
		resp["description"] = *p.Description
	} else {
		resp["description"] = nil
	}
	return resp
}

// scanPatch scans a single row from the patches table into a Patch struct.
func scanPatch(scanner interface{ Scan(dest ...any) error }) (*Patch, error) {
	p := &Patch{}
	var conflictFilesJSON sql.NullString
	err := scanner.Scan(
		&p.ID, &p.WorkspaceSlug, &p.BranchName, &p.Position,
		&p.Status, &conflictFilesJSON, &p.UpstreamPRURL, &p.Description,
		&p.AddedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if conflictFilesJSON.Valid && conflictFilesJSON.String != "" {
		_ = json.Unmarshal([]byte(conflictFilesJSON.String), &p.ConflictFiles)
	}
	return p, nil
}

// patchSelectColumns is the column list for SELECT queries on the patches table.
const patchSelectColumns = `id, workspace_slug, branch_name, position, status, conflict_files, upstream_pr_url, description, added_at, updated_at`

// getPatch retrieves a single patch by workspace slug and patch ID.
// Returns nil, nil if the patch is not found.
func getPatch(db *sql.DB, workspaceSlug, patchID string) (*Patch, error) {
	row := db.QueryRow(
		`SELECT `+patchSelectColumns+` FROM patches WHERE workspace_slug = ? AND id = ?`,
		workspaceSlug, patchID,
	)
	p, err := scanPatch(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get patch %q/%q: %w", workspaceSlug, patchID, err)
	}
	return p, nil
}

// listPatches retrieves all patches for a workspace, ordered by position ASC.
// Returns an empty slice (not nil) if no patches exist (15-REQ-9.1).
func listPatches(db *sql.DB, workspaceSlug string) ([]*Patch, error) {
	rows, err := db.Query(
		`SELECT `+patchSelectColumns+` FROM patches WHERE workspace_slug = ? ORDER BY position ASC`,
		workspaceSlug,
	)
	if err != nil {
		return nil, fmt.Errorf("list patches for %q: %w", workspaceSlug, err)
	}
	defer rows.Close()

	patches := make([]*Patch, 0)
	for rows.Next() {
		p, err := scanPatch(rows)
		if err != nil {
			return nil, fmt.Errorf("scan patch row: %w", err)
		}
		patches = append(patches, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate patch rows: %w", err)
	}
	return patches, nil
}

// getPatchByBranch retrieves a single patch by workspace slug and branch name.
// Returns nil, nil if the patch is not found.
func getPatchByBranch(db *sql.DB, workspaceSlug, branchName string) (*Patch, error) {
	row := db.QueryRow(
		`SELECT `+patchSelectColumns+` FROM patches WHERE workspace_slug = ? AND branch_name = ?`,
		workspaceSlug, branchName,
	)
	p, err := scanPatch(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get patch by branch %q/%q: %w", workspaceSlug, branchName, err)
	}
	return p, nil
}

// addPatchesBatch inserts multiple patches atomically in a single transaction.
// Patches without an explicit position are appended after the current max.
// Patches with an explicit position shift existing patches to make room.
// Uses a two-pass approach to avoid UNIQUE constraint violations on position.
// Returns the inserted patches or an error (the entire transaction rolls back).
func addPatchesBatch(db *sql.DB, patches []*Patch) ([]*Patch, error) {
	if len(patches) == 0 {
		return []*Patch{}, nil
	}

	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("batch add begin tx: %w", err)
	}
	defer tx.Rollback()

	workspaceSlug := patches[0].WorkspaceSlug

	// Load existing patches in position order.
	rows, err := tx.Query(
		`SELECT id, position FROM patches WHERE workspace_slug = ? ORDER BY position ASC`,
		workspaceSlug,
	)
	if err != nil {
		return nil, fmt.Errorf("batch add list existing: %w", err)
	}
	type idPos struct {
		id  string
		pos int
	}
	var existing []idPos
	for rows.Next() {
		var ip idPos
		if err := rows.Scan(&ip.id, &ip.pos); err != nil {
			rows.Close()
			return nil, fmt.Errorf("batch add scan existing: %w", err)
		}
		existing = append(existing, ip)
	}
	rows.Close()

	now := time.Now().UTC().Format(time.RFC3339)

	// Generate IDs and set timestamps for new patches.
	for _, p := range patches {
		if p.ID == "" {
			p.ID = uuid.New().String()
		}
		p.AddedAt = now
		p.UpdatedAt = now
		if p.Status == "" {
			p.Status = "active"
		}
	}

	// Build the merged ordering. Start with existing IDs in order.
	// Process new patches: those with explicit positions get inserted at
	// that index; those without are appended.
	order := make([]string, 0, len(existing)+len(patches))
	for _, ip := range existing {
		order = append(order, ip.id)
	}

	// Separate patches with explicit positions from appends.
	// Process explicit positions first (in batch order), then appends.
	var appends []*Patch
	for _, p := range patches {
		if p.Position > 0 && p.Position <= len(order)+1 {
			// Insert at explicit position (1-based → 0-based index).
			idx := p.Position - 1
			order = append(order, "") // grow
			copy(order[idx+1:], order[idx:])
			order[idx] = p.ID
		} else {
			appends = append(appends, p)
		}
	}
	for _, p := range appends {
		order = append(order, p.ID)
	}

	// Move all existing patches to negative temporary positions to avoid
	// UNIQUE constraint violations during reassignment.
	for i, ip := range existing {
		negPos := -(i + 1)
		if _, err := tx.Exec(
			`UPDATE patches SET position = ?, updated_at = ? WHERE workspace_slug = ? AND id = ?`,
			negPos, now, workspaceSlug, ip.id,
		); err != nil {
			return nil, fmt.Errorf("batch add temp position %q: %w", ip.id, err)
		}
	}

	// Insert new patches with negative temporary positions.
	for i, p := range patches {
		negPos := -(len(existing) + i + 1)
		_, err = tx.Exec(
			`INSERT INTO patches (id, workspace_slug, branch_name, position, status, upstream_pr_url, description, added_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.WorkspaceSlug, p.BranchName, negPos, p.Status,
			p.UpstreamPRURL, p.Description, p.AddedAt, p.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("batch insert patch[%d] %q: %w", i, p.BranchName, err)
		}
	}

	// Assign final positions according to the merged order.
	newPatchPositions := make(map[string]int, len(patches))
	for i, id := range order {
		finalPos := i + 1
		if _, err := tx.Exec(
			`UPDATE patches SET position = ? WHERE workspace_slug = ? AND id = ?`,
			finalPos, workspaceSlug, id,
		); err != nil {
			return nil, fmt.Errorf("batch add final position %q: %w", id, err)
		}
		newPatchPositions[id] = finalPos
	}

	// Update the Position field on the returned patch structs.
	for _, p := range patches {
		if pos, ok := newPatchPositions[p.ID]; ok {
			p.Position = pos
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("batch add commit: %w", err)
	}
	return patches, nil
}

// updatePatch updates mutable fields of a patch (status, description,
// upstream_pr_url, position) and refreshes updated_at.
// branch_name is immutable and ignored even if set on the input (15-REQ-10.E1).
// Returns the updated patch.
func updatePatch(db *sql.DB, p *Patch) (*Patch, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	p.UpdatedAt = now

	_, err := db.Exec(
		`UPDATE patches SET position = ?, status = ?, upstream_pr_url = ?, description = ?, updated_at = ?
		 WHERE workspace_slug = ? AND id = ?`,
		p.Position, p.Status, p.UpstreamPRURL, p.Description, p.UpdatedAt,
		p.WorkspaceSlug, p.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update patch %q: %w", p.ID, err)
	}
	return p, nil
}

// patchCount returns the number of patches for a workspace.
func patchCount(db *sql.DB, workspaceSlug string) (int, error) {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM patches WHERE workspace_slug = ?`,
		workspaceSlug,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("patch count for %q: %w", workspaceSlug, err)
	}
	return count, nil
}

// branchExistsInPatches checks whether a branch_name already exists
// in the patches for the given workspace.
func branchExistsInPatches(db *sql.DB, workspaceSlug, branchName string) (bool, error) {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM patches WHERE workspace_slug = ? AND branch_name = ?`,
		workspaceSlug, branchName,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check branch %q in %q: %w", branchName, workspaceSlug, err)
	}
	return count > 0, nil
}

// shiftPositionsDown increments the position of all patches at or after
// fromPosition for the given workspace. This is used when inserting a patch
// at a specific position to make room (15-REQ-8.2).
func shiftPositionsDown(tx *sql.Tx, workspaceSlug string, fromPosition int) error {
	_, err := tx.Exec(
		`UPDATE patches SET position = position + 1
		 WHERE workspace_slug = ? AND position >= ?`,
		workspaceSlug, fromPosition,
	)
	if err != nil {
		return fmt.Errorf("shift positions down from %d in %q: %w", fromPosition, workspaceSlug, err)
	}
	return nil
}

// compactPositions reassigns sequential 1-based positions to all patches
// in a workspace after a deletion, ordered by their current position.
// Must be called within a transaction (15-REQ-11.1).
func compactPositions(tx *sql.Tx, workspaceSlug string) error {
	rows, err := tx.Query(
		`SELECT id FROM patches WHERE workspace_slug = ? ORDER BY position ASC`,
		workspaceSlug,
	)
	if err != nil {
		return fmt.Errorf("compact positions query for %q: %w", workspaceSlug, err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("compact positions scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("compact positions iterate: %w", err)
	}

	for i, id := range ids {
		newPos := i + 1
		if _, err := tx.Exec(
			`UPDATE patches SET position = ? WHERE workspace_slug = ? AND id = ?`,
			newPos, workspaceSlug, id,
		); err != nil {
			return fmt.Errorf("compact positions update %q to %d: %w", id, newPos, err)
		}
	}
	return nil
}

// addPatchWithPosition inserts a patch with position handling within a
// transaction. If position is 0 or exceeds max+1, it appends. Otherwise,
// it shifts existing patches to make room (15-REQ-8.1, 15-REQ-8.2).
func addPatchWithPosition(db *sql.DB, p *Patch) (*Patch, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("add patch begin tx: %w", err)
	}
	defer tx.Rollback()

	// Get current max position.
	var maxPos sql.NullInt64
	err = tx.QueryRow(
		`SELECT MAX(position) FROM patches WHERE workspace_slug = ?`,
		p.WorkspaceSlug,
	).Scan(&maxPos)
	if err != nil {
		return nil, fmt.Errorf("add patch max position: %w", err)
	}

	currentMax := 0
	if maxPos.Valid {
		currentMax = int(maxPos.Int64)
	}

	// Clamp position (15-REQ-8.E2).
	if p.Position <= 0 || p.Position > currentMax+1 {
		p.Position = currentMax + 1
	}

	// Shift existing patches if inserting in the middle (15-REQ-8.2).
	if p.Position <= currentMax {
		if err := shiftPositionsDown(tx, p.WorkspaceSlug, p.Position); err != nil {
			return nil, err
		}
	}

	// Generate UUID and timestamps.
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	now := time.Now().UTC().Format(time.RFC3339)
	p.AddedAt = now
	p.UpdatedAt = now
	if p.Status == "" {
		p.Status = "active"
	}

	_, err = tx.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, upstream_pr_url, description, added_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.WorkspaceSlug, p.BranchName, p.Position, p.Status,
		p.UpstreamPRURL, p.Description, p.AddedAt, p.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert patch %q: %w", p.ID, err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("add patch commit: %w", err)
	}
	return p, nil
}

// deletePatchAndCompact removes a patch and compacts positions atomically
// within a single transaction (15-REQ-11.1, 15-REQ-11.E2).
func deletePatchAndCompact(db *sql.DB, workspaceSlug, patchID string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("delete patch begin tx: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.Exec(
		`DELETE FROM patches WHERE workspace_slug = ? AND id = ?`,
		workspaceSlug, patchID,
	)
	if err != nil {
		return fmt.Errorf("delete patch %q/%q: %w", workspaceSlug, patchID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete patch %q/%q rows affected: %w", workspaceSlug, patchID, err)
	}
	if affected == 0 {
		return fmt.Errorf("patch %q not found in workspace %q", patchID, workspaceSlug)
	}

	if err := compactPositions(tx, workspaceSlug); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete patch commit: %w", err)
	}
	return nil
}

// reorderPatches reassigns positions based on the provided ordered list of
// patch IDs within a single transaction. Validates completeness, uniqueness,
// and workspace membership (15-REQ-12.1-12.4, 15-REQ-12.E1-E3).
func reorderPatches(db *sql.DB, workspaceSlug string, orderedIDs []string) ([]*Patch, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("reorder patches begin tx: %w", err)
	}
	defer tx.Rollback()

	// Get existing patches for the workspace.
	rows, err := tx.Query(
		`SELECT id FROM patches WHERE workspace_slug = ? ORDER BY position ASC`,
		workspaceSlug,
	)
	if err != nil {
		return nil, fmt.Errorf("reorder patches query: %w", err)
	}
	defer rows.Close()

	existingIDs := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("reorder patches scan: %w", err)
		}
		existingIDs[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reorder patches iterate: %w", err)
	}

	existingCount := len(existingIDs)

	// 15-REQ-12.E1: Empty list when workspace has patches.
	if len(orderedIDs) == 0 && existingCount > 0 {
		return nil, fmt.Errorf("patch_ids list is empty but workspace has %d patches", existingCount)
	}

	// 15-REQ-12.3: Check for duplicates.
	seen := make(map[string]bool, len(orderedIDs))
	for _, id := range orderedIDs {
		if seen[id] {
			return nil, fmt.Errorf("duplicate patch ID %q in reorder list", id)
		}
		seen[id] = true
	}

	// 15-REQ-12.4: Check all IDs belong to this workspace.
	for _, id := range orderedIDs {
		if !existingIDs[id] {
			return nil, fmt.Errorf("patch ID %q does not belong to workspace %q", id, workspaceSlug)
		}
	}

	// 15-REQ-12.2: Check completeness.
	if len(orderedIDs) != existingCount {
		return nil, fmt.Errorf("patch_ids list has %d entries but workspace has %d patches", len(orderedIDs), existingCount)
	}

	// Reassign positions (15-REQ-12.1).
	// Use a two-pass approach to avoid unique constraint violations:
	// first set all to negative positions, then to the final values.
	now := time.Now().UTC().Format(time.RFC3339)

	for i, id := range orderedIDs {
		negPos := -(i + 1)
		if _, err := tx.Exec(
			`UPDATE patches SET position = ?, updated_at = ? WHERE workspace_slug = ? AND id = ?`,
			negPos, now, workspaceSlug, id,
		); err != nil {
			return nil, fmt.Errorf("reorder patches temp update %q: %w", id, err)
		}
	}

	for i, id := range orderedIDs {
		newPos := i + 1
		if _, err := tx.Exec(
			`UPDATE patches SET position = ? WHERE workspace_slug = ? AND id = ?`,
			newPos, workspaceSlug, id,
		); err != nil {
			return nil, fmt.Errorf("reorder patches final update %q: %w", id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("reorder patches commit: %w", err)
	}

	// Return the reordered patches.
	return listPatches(db, workspaceSlug)
}

// updatePatchPosition moves a patch to a new position, shifting other patches
// to maintain contiguity, within a single transaction (15-REQ-10.1).
// Uses a two-pass approach (all affected rows → negative temps → final positions)
// to avoid UNIQUE(workspace_slug, position) constraint violations.
func updatePatchPosition(db *sql.DB, workspaceSlug, patchID string, newPosition int) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("update patch position begin tx: %w", err)
	}
	defer tx.Rollback()

	// Get the current position of the patch.
	var currentPos int
	err = tx.QueryRow(
		`SELECT position FROM patches WHERE workspace_slug = ? AND id = ?`,
		workspaceSlug, patchID,
	).Scan(&currentPos)
	if err != nil {
		return fmt.Errorf("update patch position query: %w", err)
	}

	if currentPos == newPosition {
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Collect all patches for this workspace in current position order.
	rows, err := tx.Query(
		`SELECT id, position FROM patches WHERE workspace_slug = ? ORDER BY position ASC`,
		workspaceSlug,
	)
	if err != nil {
		return fmt.Errorf("update patch position list: %w", err)
	}
	type idPos struct {
		id  string
		pos int
	}
	var all []idPos
	for rows.Next() {
		var ip idPos
		if err := rows.Scan(&ip.id, &ip.pos); err != nil {
			rows.Close()
			return fmt.Errorf("update patch position scan: %w", err)
		}
		all = append(all, ip)
	}
	rows.Close()

	// Build the new ordering by removing the moving patch and inserting at newPosition.
	var others []string
	for _, ip := range all {
		if ip.id != patchID {
			others = append(others, ip.id)
		}
	}
	// Insert the moving patch at the correct index (newPosition is 1-based).
	idx := newPosition - 1
	if idx > len(others) {
		idx = len(others)
	}
	newOrder := make([]string, 0, len(others)+1)
	newOrder = append(newOrder, others[:idx]...)
	newOrder = append(newOrder, patchID)
	newOrder = append(newOrder, others[idx:]...)

	// Two-pass: first set all to negative positions.
	for i, id := range newOrder {
		negPos := -(i + 1)
		if _, err := tx.Exec(
			`UPDATE patches SET position = ?, updated_at = ? WHERE workspace_slug = ? AND id = ?`,
			negPos, now, workspaceSlug, id,
		); err != nil {
			return fmt.Errorf("update patch position temp %q: %w", id, err)
		}
	}
	// Then set final positions.
	for i, id := range newOrder {
		finalPos := i + 1
		if _, err := tx.Exec(
			`UPDATE patches SET position = ? WHERE workspace_slug = ? AND id = ?`,
			finalPos, workspaceSlug, id,
		); err != nil {
			return fmt.Errorf("update patch position final %q: %w", id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("update patch position commit: %w", err)
	}
	return nil
}

package campaign

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// Handler implements the campaign REST API endpoints.
type Handler struct {
	store         *Store
	scheduler     *Scheduler
	db            *sql.DB
	gitOps        GitOps
	rebaseEngine  *RebaseEngine
	authz         *Authz
	workspaceRoot string
}

// NewHandler creates a new campaign Handler.
func NewHandler(db *sql.DB) *Handler {
	store := NewStore(db)
	return &Handler{
		store:     store,
		scheduler: NewScheduler(store),
		db:        db,
	}
}

// RegisterRoutes registers campaign API routes on the given echo group.
// The group should be mounted at /api/v1.
func RegisterRoutes(g *echo.Group, db *sql.DB) error {
	h := NewHandler(db)

	campaigns := g.Group("/workspaces/:slug/campaigns")
	campaigns.POST("", h.createCampaign)
	campaigns.GET("", h.listCampaigns)
	campaigns.GET("/:id", h.getCampaign)
	campaigns.DELETE("/:id", h.cancelCampaign)
	campaigns.POST("/:id/specs/:spec_id/resolve", h.resolveSpec)

	return nil
}

// respondError writes a flat JSON error response: {"error": "message"}.
func respondError(c echo.Context, code int, message string) error {
	return c.JSON(code, map[string]string{"error": message})
}

// createCampaignRequest is the expected request body for POST /campaigns.
type createCampaignRequest struct {
	Name              string   `json:"name"`
	SpecIDs           []string `json:"spec_ids"`
	IntegrationBranch string   `json:"integration_branch"`
}

// campaignResponse is the JSON response body for campaign endpoints.
type campaignResponse struct {
	ID                string         `json:"id"`
	WorkspaceSlug     string         `json:"workspace_slug"`
	Name              string         `json:"name"`
	IntegrationBranch string         `json:"integration_branch"`
	Status            string         `json:"status"`
	DAG               *DAG           `json:"dag,omitempty"`
	Specs             []CampaignSpec `json:"specs"`
	Warnings          []string       `json:"warnings,omitempty"`
	CreatedBy         string         `json:"created_by"`
	CreatedAt         string         `json:"created_at"`
	UpdatedAt         string         `json:"updated_at"`
}

func (h *Handler) createCampaign(c echo.Context) error {
	ctx := c.Request().Context()
	slug := c.Param("slug")

	// Auth check.
	auth := apikit.GetAuthInfo(c)
	if auth == nil {
		return respondError(c, http.StatusUnauthorized, "authentication required")
	}
	if isPAT(auth) && !hasCampaignWriteAccess(auth) {
		return respondError(c, http.StatusForbidden, "campaigns:write scope required")
	}

	// Parse request body.
	var req createCampaignRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return respondError(c, http.StatusBadRequest, "invalid request body")
	}

	// Validate required fields.
	if req.Name == "" {
		return respondError(c, http.StatusUnprocessableEntity, "name is required")
	}
	if req.IntegrationBranch == "" {
		return respondError(c, http.StatusUnprocessableEntity, "integration_branch is required")
	}

	// Check for active campaign on the same integration branch.
	hasActive, err := h.store.HasActiveCampaignForBranch(ctx, slug, req.IntegrationBranch)
	if err != nil {
		return respondError(c, http.StatusInternalServerError, "internal server error")
	}
	if hasActive {
		return respondError(c, http.StatusConflict, "active campaign already exists for this integration branch")
	}

	// Check campaign name uniqueness.
	existing, err := h.store.GetCampaignByName(ctx, slug, req.Name)
	if err != nil {
		return respondError(c, http.StatusInternalServerError, "internal server error")
	}
	if existing != nil {
		return respondError(c, http.StatusConflict, "campaign name already exists in this workspace")
	}

	// Determine spec IDs and build DAG.
	var warnings []string
	var specIDs []string
	var specDirMap map[string]string // specID → specDirName

	if len(req.SpecIDs) == 0 {
		// Implicit discovery.
		discovered, discWarnings, err := DiscoverPendingSpecs(ctx, h.workspaceRoot, slug)
		if err != nil {
			return respondError(c, http.StatusUnprocessableEntity, err.Error())
		}
		specIDs = discovered
		warnings = append(warnings, discWarnings...)
		specDirMap = h.buildSpecDirMap(slug, specIDs)
	} else {
		// Explicit spec IDs.
		specDirMap = h.buildSpecDirMap(slug, req.SpecIDs)
		specIDs = req.SpecIDs
	}

	if len(specIDs) == 0 {
		return respondError(c, http.StatusUnprocessableEntity, "no valid specs found")
	}

	// Build DAG from tasks.json dependencies.
	reader := h.newFileTasksReader(slug, specDirMap)
	dag, err := BuildDAG(ctx, specIDs, reader)
	if err != nil {
		return respondError(c, http.StatusUnprocessableEntity, err.Error())
	}

	// Compute the initial frontier.
	frontier := ComputeFrontier(dag, map[string]bool{})

	// Resolve repo path.
	repoPath := filepath.Join(h.workspaceRoot, slug, "trunk")

	// Create spec branches for frontier specs.
	type branchInfo struct {
		specID     string
		branchName string
		sha        string
	}
	var createdBranches []branchInfo

	if h.gitOps != nil {
		frontierSet := make(map[string]bool, len(frontier))
		for _, s := range frontier {
			frontierSet[s] = true
		}

		for _, specID := range frontier {
			dirName := specDirMap[specID]
			if dirName == "" {
				dirName = specID
			}
			branchName := DeriveSpecBranchName(dirName)
			sha, err := h.gitOps.CreateBranch(ctx, repoPath, branchName, req.IntegrationBranch)
			if err != nil {
				// Rollback: delete already-created branches.
				for _, b := range createdBranches {
					_ = h.gitOps.DeleteBranch(ctx, repoPath, b.branchName)
				}
				return respondError(c, http.StatusInternalServerError, fmt.Sprintf("failed to create branch %s: %v", branchName, err))
			}
			createdBranches = append(createdBranches, branchInfo{
				specID:     specID,
				branchName: branchName,
				sha:        sha,
			})
		}
	}

	// Build branch lookup for spec response.
	branchLookup := make(map[string]branchInfo, len(createdBranches))
	for _, b := range createdBranches {
		branchLookup[b.specID] = b
	}

	// Persist campaign.
	now := time.Now().UTC().Format(time.RFC3339)
	campaign := &Campaign{
		ID:                uuid.New().String(),
		WorkspaceSlug:     slug,
		Name:              req.Name,
		IntegrationBranch: req.IntegrationBranch,
		Status:            "active",
		DAG:               dag,
		CreatedBy:         resolveCreatedBy(auth),
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	if err := h.store.CreateCampaign(ctx, campaign); err != nil {
		// Rollback branches.
		if h.gitOps != nil {
			for _, b := range createdBranches {
				_ = h.gitOps.DeleteBranch(ctx, repoPath, b.branchName)
			}
		}
		return respondError(c, http.StatusInternalServerError, "failed to create campaign")
	}

	// Persist campaign specs.
	frontierSet := make(map[string]bool, len(frontier))
	for _, s := range frontier {
		frontierSet[s] = true
	}

	var campaignSpecs []CampaignSpec
	for _, specID := range specIDs {
		status := "pending"
		var branchName, branchSHA string
		if frontierSet[specID] {
			status = "active"
			if b, ok := branchLookup[specID]; ok {
				branchName = b.branchName
				branchSHA = b.sha
			}
		}

		cs := CampaignSpec{
			CampaignID: campaign.ID,
			SpecID:     specID,
			Status:     status,
			BranchName: branchName,
			BranchSHA:  branchSHA,
			UpdatedAt:  now,
		}
		if err := h.store.CreateCampaignSpec(ctx, &cs); err != nil {
			return respondError(c, http.StatusInternalServerError, "failed to create campaign spec")
		}
		campaignSpecs = append(campaignSpecs, cs)
	}

	resp := campaignResponse{
		ID:                campaign.ID,
		WorkspaceSlug:     campaign.WorkspaceSlug,
		Name:              campaign.Name,
		IntegrationBranch: campaign.IntegrationBranch,
		Status:            campaign.Status,
		DAG:               campaign.DAG,
		Specs:             campaignSpecs,
		Warnings:          warnings,
		CreatedBy:         campaign.CreatedBy,
		CreatedAt:         campaign.CreatedAt,
		UpdatedAt:         campaign.UpdatedAt,
	}

	return c.JSON(http.StatusCreated, resp)
}

// resolveCreatedBy extracts a user identifier from auth info.
func resolveCreatedBy(auth *apikit.AuthInfo) string {
	if auth.UserID != "" {
		return auth.UserID
	}
	return auth.CredentialType
}

// buildSpecDirMap scans the workspace's specs directory and builds a map
// from spec ID to spec directory name (e.g., "07" → "07_secrets_variables").
func (h *Handler) buildSpecDirMap(slug string, specIDs []string) map[string]string {
	result := make(map[string]string, len(specIDs))
	if h.workspaceRoot == "" {
		return result
	}

	specsDir := filepath.Join(h.workspaceRoot, slug, "trunk", ".agent-fox", "specs")
	entries, err := readDirSafe(specsDir)
	if err != nil {
		return result
	}

	// Build lookup from spec ID to directory name.
	specIDSet := make(map[string]bool, len(specIDs))
	for _, id := range specIDs {
		specIDSet[id] = true
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dirName := e.Name()
		specID := ExtractSpecID(dirName)
		if specID != "" && specIDSet[specID] {
			result[specID] = dirName
		}
	}
	return result
}

// fileTasksReader reads tasks.json from the filesystem for DAG construction.
type fileTasksReader struct {
	specsDir string
	dirMap   map[string]string // specID → dirName
}

// newFileTasksReader creates a TasksReader that reads from the filesystem.
func (h *Handler) newFileTasksReader(slug string, dirMap map[string]string) TasksReader {
	specsDir := ""
	if h.workspaceRoot != "" {
		specsDir = filepath.Join(h.workspaceRoot, slug, "trunk", ".agent-fox", "specs")
	}
	return &fileTasksReader{specsDir: specsDir, dirMap: dirMap}
}

func (f *fileTasksReader) ReadTasksJSON(_ context.Context, specID string) (*TasksJSON, error) {
	dirName, ok := f.dirMap[specID]
	if !ok || f.specsDir == "" {
		return &TasksJSON{}, nil // No directory mapping; treat as no dependencies.
	}

	path := filepath.Join(f.specsDir, dirName, "tasks.json")
	data, err := readFileSafe(path)
	if err != nil {
		return &TasksJSON{}, nil // Missing file; treat as no dependencies.
	}

	var tj TasksJSON
	if err := json.Unmarshal(data, &tj); err != nil {
		return nil, fmt.Errorf("malformed tasks.json for spec %s: %w", specID, err)
	}
	return &tj, nil
}

func (h *Handler) listCampaigns(c echo.Context) error {
	return c.JSON(http.StatusNotImplemented, map[string]string{
		"error": "not implemented",
	})
}

func (h *Handler) getCampaign(c echo.Context) error {
	return c.JSON(http.StatusNotImplemented, map[string]string{
		"error": "not implemented",
	})
}

func (h *Handler) cancelCampaign(c echo.Context) error {
	return c.JSON(http.StatusNotImplemented, map[string]string{
		"error": "not implemented",
	})
}

func (h *Handler) resolveSpec(c echo.Context) error {
	return c.JSON(http.StatusNotImplemented, map[string]string{
		"error": "not implemented",
	})
}

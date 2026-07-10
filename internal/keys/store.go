package keys

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
)

// CreateKey generates a new API key for the given user and stores it in the
// database. It returns the KeyRecord, the plaintext secret (for one-time
// delivery to the caller), and any error.
//
// The plaintext secret is never written to persistent storage; only its
// SHA-256 hash is stored in the api_keys table.
//
// expiresInDays controls the key lifetime: 0 means indefinite (expires_at is
// NULL), otherwise expires_at is set to now + expiresInDays * 24h.
func CreateKey(ctx context.Context, tx *sql.Tx, userID string, expiresInDays int) (*KeyRecord, string, error) {
	keyID, err := GenerateKeyID()
	if err != nil {
		return nil, "", fmt.Errorf("generate key_id: %w", err)
	}

	secret, err := GenerateSecret()
	if err != nil {
		return nil, "", fmt.Errorf("generate secret: %w", err)
	}

	secretHash := HashSecret(secret)
	now := time.Now().UTC()
	createdAt := now.Format(time.RFC3339)

	expiresAt := ComputeExpiresAt(expiresInDays, now)
	var expiresAtStr *string
	if expiresAt != nil {
		s := expiresAt.Format(time.RFC3339)
		expiresAtStr = &s
	}

	id := uuid.New().String()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO api_keys (id, key_id, secret_hash, user_id, expires_at, created_at, revoked_at)
		 VALUES (?, ?, ?, ?, ?, ?, NULL)`,
		id, keyID, secretHash, userID, expiresAtStr, createdAt,
	)
	if err != nil {
		return nil, "", fmt.Errorf("insert api_key: %w", err)
	}

	rec := &KeyRecord{
		KeyID:     keyID,
		UserID:    userID,
		CreatedAt: createdAt,
		ExpiresAt: expiresAtStr,
	}

	return rec, secret, nil
}

// RevokeActiveKey sets revoked_at on all non-revoked API keys for the given
// user. This is a no-op if no active key exists — it does not return an error
// in that case.
func RevokeActiveKey(ctx context.Context, tx *sql.Tx, userID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := tx.ExecContext(ctx,
		`UPDATE api_keys SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`,
		now, userID,
	)
	if err != nil {
		return fmt.Errorf("revoke active keys: %w", err)
	}
	return nil
}

// StoreRefreshKey generates a new secret for an existing non-revoked key,
// stores the SHA-256 hash, recalculates expires_at using the original expiry
// duration, and returns the updated KeyRecord plus the plaintext secret.
//
// Returns sql.ErrNoRows if the key_id does not exist or has been revoked.
//
// Note on expiry duration calculation: the original duration is computed as
// ceil((expires_at - created_at) / 24h). This works correctly for the first
// refresh. See docs/errata/02_user_management_divergences.md section 4 for
// the limitation on subsequent refreshes.
func StoreRefreshKey(ctx context.Context, db *sql.DB, keyID string) (*KeyRecord, string, error) {
	// Look up the key; revoked keys are treated as non-existent.
	var userID, createdAt string
	var expiresAt, revokedAt sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT user_id, created_at, expires_at, revoked_at
		 FROM api_keys WHERE key_id = ?`,
		keyID,
	).Scan(&userID, &createdAt, &expiresAt, &revokedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, "", sql.ErrNoRows
		}
		return nil, "", fmt.Errorf("query api_key: %w", err)
	}

	// Revoked keys are treated as non-existent for refresh.
	if revokedAt.Valid && revokedAt.String != "" {
		return nil, "", sql.ErrNoRows
	}

	// Compute original expiry duration in days.
	originalDays := 0
	if expiresAt.Valid && expiresAt.String != "" {
		createdTime, err := time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return nil, "", fmt.Errorf("parse created_at: %w", err)
		}
		expiresTime, err := time.Parse(time.RFC3339, expiresAt.String)
		if err != nil {
			return nil, "", fmt.Errorf("parse expires_at: %w", err)
		}
		durationHours := expiresTime.Sub(createdTime).Hours()
		originalDays = max(int(math.Ceil(durationHours/24)), 1)
	}

	// Generate new secret.
	newSecret, err := GenerateSecret()
	if err != nil {
		return nil, "", fmt.Errorf("generate secret: %w", err)
	}

	newHash := HashSecret(newSecret)
	now := time.Now().UTC()

	// Compute new expires_at.
	newExpiresAt := ComputeExpiresAt(originalDays, now)
	var newExpiresAtStr *string
	if newExpiresAt != nil {
		s := newExpiresAt.Format(time.RFC3339)
		newExpiresAtStr = &s
	}

	// Update the key in the database.
	_, err = db.ExecContext(ctx,
		`UPDATE api_keys SET secret_hash = ?, expires_at = ? WHERE key_id = ?`,
		newHash, newExpiresAtStr, keyID,
	)
	if err != nil {
		return nil, "", fmt.Errorf("update api_key: %w", err)
	}

	rec := &KeyRecord{
		KeyID:     keyID,
		UserID:    userID,
		CreatedAt: createdAt,
		ExpiresAt: newExpiresAtStr,
	}

	return rec, newSecret, nil
}

// StoreRevokeKey sets revoked_at on a specific key by key_id. It is
// idempotent: if the key is already revoked, the existing revoked_at timestamp
// is preserved and no error is returned. Returns sql.ErrNoRows if the key_id
// does not exist.
func StoreRevokeKey(ctx context.Context, db *sql.DB, keyID string) error {
	var revokedAt sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT revoked_at FROM api_keys WHERE key_id = ?`,
		keyID,
	).Scan(&revokedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return sql.ErrNoRows
		}
		return fmt.Errorf("query api_key: %w", err)
	}

	// Already revoked — idempotent no-op.
	if revokedAt.Valid && revokedAt.String != "" {
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = db.ExecContext(ctx,
		`UPDATE api_keys SET revoked_at = ? WHERE key_id = ? AND revoked_at IS NULL`,
		now, keyID,
	)
	if err != nil {
		return fmt.Errorf("revoke api_key: %w", err)
	}
	return nil
}

// LookupKeyOwner returns the user_id for a given key_id, or sql.ErrNoRows if
// the key does not exist.
func LookupKeyOwner(ctx context.Context, db *sql.DB, keyID string) (string, error) {
	var userID string
	err := db.QueryRowContext(ctx,
		`SELECT user_id FROM api_keys WHERE key_id = ?`,
		keyID,
	).Scan(&userID)
	if err != nil {
		return "", err
	}
	return userID, nil
}

package sandboxes

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/txsvc/apikit"
)

// ErrLeaseNotFound is the one error the lease store reports for a lease that
// cannot be found or used, so that a caller cannot tell why.
var ErrLeaseNotFound = errors.New("sandbox lease not found")

// Lease states. A lease is live while it is leased or active; the gateway moves
// it from leased to active or to failed, and the others end it.
const (
	leaseLeased    = "leased"
	leaseActive    = "active"
	leaseDestroyed = "destroyed"
	leaseExpired   = "expired"
	leaseFailed    = "failed"
)

// Lease is a caller's right to one sandbox, with a snapshot of the profile
// taken when it was issued. It carries neither the token nor its hash.
type Lease struct {
	ID              string
	Workspace       string
	Profile         string
	Purpose         string
	State           string
	Image           string
	Network         string
	Resources       Resources
	RequireLandlock bool
	Secrets         []string
	OptionalSecrets []string
	Env             map[string]string
	Labels          map[string]string
	TTLSeconds      int64
	ExpiresAt       string
	CreatedBy       string
	CreatedAt       string
	UpdatedAt       string
	SandboxID       *string
	Error           *string
	EndedAt         *string
	EndReason       *string
}

// dbtx is what the store needs of a database or a transaction.
type dbtx interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
}

// LeaseStore reads and writes leases. now stamps the changes it makes.
type LeaseStore struct {
	db  *sql.DB
	now func() time.Time
}

// NewLeaseStore returns a store over db that stamps changes with now.
func NewLeaseStore(db *sql.DB, now func() time.Time) *LeaseStore {
	return &LeaseStore{db: db, now: now}
}

// tokenShape is the form of a lease token: sbl_ and 44 characters of
// base64.RawURLEncoding.
var tokenShape = regexp.MustCompile(`^sbl_[A-Za-z0-9_-]{44}$`)

// HashToken returns the lowercase hex SHA-256 of token, the only form of a
// token that hub stores.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// leaseColumns are the columns a Lease is read from, in scan order.
const leaseColumns = `id, workspace, profile, purpose, state, image, network, cpus, memory_mb, pids, disk_mb, require_landlock, secrets, optional_secrets, env, labels, ttl_seconds, expires_at, created_by, created_at, updated_at, sandbox_id, error, ended_at, end_reason`

// rowScanner is a *sql.Row or *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

// scanLease reads one row selected with leaseColumns.
func scanLease(row rowScanner) (*Lease, error) {
	var (
		l                                          Lease
		landlock                                   int
		secretsJSON, optionalJSON, envJSON, labels string
		sandboxID, errText, endedAt, endReason     sql.NullString
	)
	if err := row.Scan(&l.ID, &l.Workspace, &l.Profile, &l.Purpose, &l.State, &l.Image, &l.Network,
		&l.Resources.CPUs, &l.Resources.MemoryMB, &l.Resources.PIDs, &l.Resources.DiskMB,
		&landlock, &secretsJSON, &optionalJSON, &envJSON, &labels, &l.TTLSeconds, &l.ExpiresAt, &l.CreatedBy,
		&l.CreatedAt, &l.UpdatedAt, &sandboxID, &errText, &endedAt, &endReason); err != nil {
		return nil, err
	}
	l.RequireLandlock = landlock != 0
	for _, col := range []struct {
		src string
		dst any
	}{{secretsJSON, &l.Secrets}, {optionalJSON, &l.OptionalSecrets}, {envJSON, &l.Env}, {labels, &l.Labels}} {
		if err := json.Unmarshal([]byte(col.src), col.dst); err != nil {
			return nil, fmt.Errorf("decoding a JSON column of lease %s: %w", l.ID, err)
		}
	}
	if l.Secrets == nil {
		l.Secrets = []string{}
	}
	if l.OptionalSecrets == nil {
		l.OptionalSecrets = []string{}
	}
	if l.Env == nil {
		l.Env = map[string]string{}
	}
	if l.Labels == nil {
		l.Labels = map[string]string{}
	}
	l.SandboxID, l.Error, l.EndedAt, l.EndReason = nullable(sandboxID), nullable(errText), nullable(endedAt), nullable(endReason)
	return &l, nil
}

// nullable returns a pointer to the value of s, or nil when s is NULL.
func nullable(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}

// InsertLease stores l together with the hash of its token, through x, which
// may be a transaction. The caller sets every field of l; the store adds none.
func (s *LeaseStore) InsertLease(x dbtx, l *Lease, tokenHash string) error {
	secretsJSON, err := json.Marshal(nonNilStrings(l.Secrets))
	if err != nil {
		return err
	}
	optionalJSON, err := json.Marshal(nonNilStrings(l.OptionalSecrets))
	if err != nil {
		return err
	}
	envJSON, err := json.Marshal(nonNilMap(l.Env))
	if err != nil {
		return err
	}
	labelsJSON, err := json.Marshal(nonNilMap(l.Labels))
	if err != nil {
		return err
	}
	var hash any
	if tokenHash != "" {
		hash = tokenHash
	}
	_, err = x.Exec(`INSERT INTO sandbox_leases (`+leaseColumns+`, token_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ID, l.Workspace, l.Profile, l.Purpose, l.State, l.Image, l.Network,
		l.Resources.CPUs, l.Resources.MemoryMB, l.Resources.PIDs, l.Resources.DiskMB,
		boolToInt(l.RequireLandlock), string(secretsJSON), string(optionalJSON), string(envJSON), string(labelsJSON),
		l.TTLSeconds, l.ExpiresAt, l.CreatedBy, l.CreatedAt, l.UpdatedAt,
		l.SandboxID, l.Error, l.EndedAt, l.EndReason, hash)
	return err
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// GetLease returns the lease with the given id, or ErrLeaseNotFound.
func (s *LeaseStore) GetLease(id string) (*Lease, error) {
	l, err := scanLease(s.db.QueryRow(`SELECT `+leaseColumns+` FROM sandbox_leases WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrLeaseNotFound
	}
	return l, err
}

// LookupByToken returns the lease a presented token belongs to. It returns
// the lease only when the token is well formed, its hash matches and the lease
// is live (leased or active). Every other case returns (nil, ErrLeaseNotFound),
// without saying which, so a caller learns nothing from a refusal. This is the
// lookup the sandbox gateway authenticates with.
func (s *LeaseStore) LookupByToken(token string) (*Lease, error) {
	if !tokenShape.MatchString(token) {
		return nil, ErrLeaseNotFound
	}
	l, err := scanLease(s.db.QueryRow(`SELECT `+leaseColumns+` FROM sandbox_leases
		WHERE token_hash = ? AND state IN ('leased', 'active')`, HashToken(token)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrLeaseNotFound
	}
	return l, err
}

// terminalStates are the states a lease ends in.
var terminalStates = []string{leaseDestroyed, leaseExpired, leaseFailed}

// EndLease ends a live lease (leased or active) in the terminal state target
// with the given reason, stamps ended_at and updated_at, and clears the token
// hash, all in one conditional UPDATE, so that a lease that has ended already is
// not touched. It reports whether it changed a row. Revoking a token and ending
// a lease are the same act.
func (s *LeaseStore) EndLease(l *Lease, target, reason string) (bool, error) {
	if !slices.Contains(terminalStates, target) {
		return false, fmt.Errorf("sandbox lease: %q is not a terminal state", target)
	}
	return s.endLease(l.ID, []string{leaseLeased, leaseActive}, target, &reason, nil)
}

// endLease is the one place a lease ends: every ending goes through this
// statement. It moves the lease from one of the states in from to the terminal
// state target, records the end reason and, when message is not nil, the error
// text, stamps ended_at and updated_at and clears the token hash, all in one
// UPDATE conditioned on the state. It reports whether it changed a row.
func (s *LeaseStore) endLease(id string, from []string, target string, reason, message *string) (bool, error) {
	now := apikit.FormatUTC(s.now())
	args := []any{target, now, now, optional(reason), optional(message), id}
	for _, state := range from {
		args = append(args, state)
	}
	res, err := s.db.Exec(`UPDATE sandbox_leases
		SET state = ?, ended_at = ?, updated_at = ?, end_reason = ?, error = COALESCE(?, error), token_hash = NULL
		WHERE id = ? AND state IN (`+strings.TrimSuffix(strings.Repeat("?, ", len(from)), ", ")+`)`, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// optional returns the value s points to, or nil, which SQL stores as NULL.
func optional(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// RevokeToken revokes the token of the lease with the given id by ending the
// lease, which clears the hash in the same update.
func (s *LeaseStore) RevokeToken(id string) error {
	_, err := s.EndLease(&Lease{ID: id}, leaseDestroyed, "revoked")
	return err
}

// countLive returns the number of leases that count against capacity: those
// leased or active whose expires_at is later than now, in every workspace. A
// lease past its expires_at does not count although nothing has ended it yet,
// so a caller that crashed cannot hold capacity for good. The timestamps are
// RFC 3339 UTC strings of one width, so comparing them as text compares time.
func (s *LeaseStore) countLive(x dbtx, now time.Time) (int, error) {
	var n int
	err := x.QueryRow(`SELECT COUNT(*) FROM sandbox_leases WHERE state IN ('leased', 'active') AND expires_at > ?`,
		apikit.FormatUTC(now)).Scan(&n)
	return n, err
}

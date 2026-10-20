package sandboxes

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// LeaseConfig is what the lease service is built from.
type LeaseConfig struct {
	// Service holds the validated profiles, the default among them and the
	// limit on live leases.
	Service *Service
	DB      *sql.DB
	// Secrets lists secret names for the presence check.
	Secrets SecretLister
	// Audit receives the lease event; nil means none.
	Audit audit.Emitter
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Rand is the source of ids and tokens; crypto/rand when nil.
	Rand io.Reader
	// GatewayURL is the address lease holders dial, which every lease shows.
	GatewayURL string
}

// LeaseService issues, reads and ends leases. It needs no provider: a lease is a
// record, and no sandbox exists until the gateway creates one.
type LeaseService struct {
	profiles *Service
	db       *sql.DB
	// issueMu serialises the capacity count and the insert. Hub runs as one
	// replica on one SQLite file, so a lock in the process is enough. Hub's own
	// database handle also begins its transactions IMMEDIATE; the lock keeps the
	// limit with a handle whose transactions are deferred, which would let two
	// requests both pass the count.
	issueMu sync.Mutex
	store   *LeaseStore
	secrets SecretLister
	audit   audit.Emitter
	now     func() time.Time
	rand    io.Reader
	// gatewayURL is where a lease holder dials the gateway.
	gatewayURL string
}

// NewLeaseService returns a lease service built from cfg.
func NewLeaseService(cfg LeaseConfig) *LeaseService {
	s := &LeaseService{
		profiles: cfg.Service,
		db:       cfg.DB,
		secrets:  cfg.Secrets,
		audit:    cfg.Audit,
		now:      cfg.Now,
		rand:     cfg.Rand,

		gatewayURL: cfg.GatewayURL,
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.rand == nil {
		s.rand = rand.Reader
	}
	s.store = NewLeaseStore(cfg.DB, s.now)
	return s
}

// CapacityError reports that the deployment's limit on live leases is reached.
type CapacityError struct{ Limit int }

func (e *CapacityError) Error() string {
	return fmt.Sprintf("sandbox capacity reached: at most %d leases may be live at once (max_sandboxes)", e.Limit)
}

// issueParams are the validated inputs of Issue.
type issueParams struct {
	profile ValidatedProfile
	purpose string
	ttl     time.Duration
	labels  map[string]string
}

// defaultProfile returns the profile marked as the default.
func (s *LeaseService) defaultProfile() (ValidatedProfile, bool) {
	for _, p := range s.profiles.Profiles() {
		if p.Default {
			return p, true
		}
	}
	return ValidatedProfile{}, false
}

// profileByName returns the profile with exactly this name.
func (s *LeaseService) profileByName(name string) (ValidatedProfile, bool) {
	for _, p := range s.profiles.Profiles() {
		if p.Name == name {
			return p, true
		}
	}
	return ValidatedProfile{}, false
}

// leaseCreator names who issued a lease: "admin" for an admin token, the user's
// ID for every other credential.
func leaseCreator(auth *apikit.AuthInfo) string {
	if isAdmin(auth) {
		return "admin"
	}
	return auth.UserID
}

// Issue stores a new lease in state leased and returns it with its token, which
// is the only time the token exists in plaintext.
//
// It first confirms that every secret the profile requires exists for the
// workspace, by name. It then draws the id and the token, snapshots the
// profile onto the lease, and stores the lease with only the token's hash. The
// clock is cut to whole seconds, as are the lease's timestamps and TTL, so that
// expires_at minus created_at is exactly ttl_seconds.
func (s *LeaseService) Issue(ctx context.Context, slug string, auth *apikit.AuthInfo, p issueParams) (*Lease, string, error) {
	if err := CheckRequiredSecrets(s.secrets, s.db, slug, p.profile.Secrets); err != nil {
		return nil, "", err
	}

	id, err := generateLeaseID(s.rand)
	if err != nil {
		return nil, "", err
	}
	token, err := generateToken(s.rand)
	if err != nil {
		return nil, "", err
	}

	now := s.now().UTC().Truncate(time.Second)
	ttl := p.ttl.Truncate(time.Second)
	stamp := apikit.FormatUTC(now)
	lease := &Lease{
		ID:              id,
		Workspace:       slug,
		Profile:         p.profile.Name,
		Purpose:         p.purpose,
		State:           "leased",
		Image:           p.profile.Image,
		Network:         p.profile.Network,
		Resources:       p.profile.Resources,
		RequireLandlock: p.profile.RequireLandlock,
		Secrets:         slices.Clone(nonNilStrings(p.profile.Secrets)),
		OptionalSecrets: slices.Clone(nonNilStrings(p.profile.OptionalSecrets)),
		Env:             maps.Clone(nonNilMap(p.profile.Env)),
		Labels:          maps.Clone(nonNilMap(p.labels)),
		TTLSeconds:      int64(ttl / time.Second),
		ExpiresAt:       apikit.FormatUTC(now.Add(ttl)),
		CreatedBy:       leaseCreator(auth),
		CreatedAt:       stamp,
		UpdatedAt:       stamp,
	}

	if err := s.insertWithinCapacity(ctx, lease, HashToken(token)); err != nil {
		return nil, "", err
	}
	s.emitIssued(ctx, auth, lease, ttl)
	return lease, token, nil
}

// emitIssued records a hub.sandbox.lease event for a lease that has just been
// stored. The event names the secrets, never their values, and holds neither the
// token nor the fixed environment. Without an emitter nothing is recorded, and a
// failed emit is logged and does not undo the lease.
func (s *LeaseService) emitIssued(ctx context.Context, auth *apikit.AuthInfo, lease *Lease, ttl time.Duration) {
	if s.audit == nil {
		return
	}
	event := audit.HubEvent{
		EventType:    audit.EventSandboxLease,
		ResourceType: "sandbox",
		ResourceID:   lease.ID,
		Action:       "lease",
		Workspace:    lease.Workspace,
		ActorID:      auth.UserID,
		ActorType:    auth.CredentialType,
		Metadata: map[string]any{
			"lease_id":              lease.ID,
			"profile":               lease.Profile,
			"purpose":               lease.Purpose,
			"ttl":                   ttl.String(),
			"secret_names":          lease.Secrets,
			"optional_secret_names": lease.OptionalSecrets,
		},
	}
	if err := s.audit.Emit(ctx, event); err != nil {
		slog.Error("audit: failed to emit "+audit.EventSandboxLease,
			"workspace", lease.Workspace, "lease_id", lease.ID, "error", err)
	}
}

// insertWithinCapacity stores the lease unless max_sandboxes leases are live
// already, in which case it returns a *CapacityError. The count and the insert
// happen under one lock in the process and in one transaction, so concurrent
// issuers can never overshoot the limit; any failure rolls back, releases the
// lock and leaves no partial row.
func (s *LeaseService) insertWithinCapacity(ctx context.Context, lease *Lease, tokenHash string) error {
	s.issueMu.Lock()
	defer s.issueMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // a no-op once Commit has succeeded

	now, err := apikit.ParseUTC(lease.CreatedAt)
	if err != nil {
		return err
	}
	live, err := s.store.countLive(tx, now)
	if err != nil {
		return err
	}
	if limit := s.profiles.MaxSandboxes(); live >= limit {
		return &CapacityError{Limit: limit}
	}
	if err := s.store.InsertLease(tx, lease, tokenHash); err != nil {
		return err
	}
	return tx.Commit()
}

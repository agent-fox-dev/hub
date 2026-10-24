package sandboxes

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/hub/internal/scrub"
	"github.com/txsvc/apikit"
)

// tokenLookup is what the authenticator needs of the lease store.
type tokenLookup interface {
	LookupByToken(token string) (*Lease, error)
}

// NewLeaseAuthenticator returns the authenticator hub gives gatewayd. gatewayd
// calls it once for every RPC with the token the caller presented; it accepts
// the token, naming the lease id as the principal, only when the token belongs
// to a lease that is leased or active and whose expires_at is later than now.
//
// It reads the lease store on every call and keeps nothing, so a lease that
// DELETE or a failed Create has ended is refused on the very next RPC, the
// relayed calls on a box that already exists included. The lookup is by the
// SHA-256 of the token, an indexed read, and the token is never compared in
// the process. A refusal says nothing about why. The authenticator never logs a
// token or its hash.
func NewLeaseAuthenticator(store *LeaseStore, now func() time.Time) gatewayd.Authenticator {
	return newLeaseAuthenticator(store, now)
}

// newLeaseAuthenticator is NewLeaseAuthenticator over any token lookup.
func newLeaseAuthenticator(lookup tokenLookup, now func() time.Time) gatewayd.Authenticator {
	return func(_ context.Context, token string) (string, bool) {
		lease, err := lookup.LookupByToken(token)
		if err != nil {
			if !errors.Is(err, ErrLeaseNotFound) {
				// The error comes from the database. It is scrubbed all the same:
				// whether a driver quotes its arguments, which hold the hash, is not
				// for hub to find out.
				slog.Error("sandbox gateway: looking up a lease token failed",
					"error", scrub.Text(err.Error(), token, HashToken(token)))
			}
			return "", false
		}
		if lease.State != leaseLeased && lease.State != leaseActive {
			return "", false
		}
		expires, err := apikit.ParseUTC(lease.ExpiresAt)
		if err != nil {
			slog.Error("sandbox gateway: a lease has an expires_at that cannot be read", "lease_id", lease.ID, "error", err)
			return "", false
		}
		if !expires.After(now()) {
			return "", false
		}
		return lease.ID, true
	}
}

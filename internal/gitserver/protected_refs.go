package gitserver

import (
	"errors"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
)

// protectedRefPrefix is the ref namespace reserved for hub-internal backup
// refs. Client pushes that create, update or delete refs under this prefix
// are rejected before the pre-receive hook is consulted.
const protectedRefPrefix = "refs/hub/replaced/"

// protectedRefMessage is the per-ref error message returned to the client
// when a push targets a ref under the protected namespace.
const protectedRefMessage = "refs/hub/replaced/ is maintained by the hub and cannot be pushed to"

// errProtectedRef is the error returned by the storer interception when a
// client push targets a ref under the protected namespace. go-git records
// it as the per-ref status in the report, so the client sees it as
// "ng <ref> <message>".
var errProtectedRef = errors.New(protectedRefMessage)

// isProtectedRef returns true when the ref name falls under the protected
// backup-ref namespace. Only the exact prefix "refs/hub/replaced/" is
// matched; other names under refs/hub/ (e.g. refs/hub/forward/) are not
// covered.
func isProtectedRef(name plumbing.ReferenceName) bool {
	return strings.HasPrefix(string(name), protectedRefPrefix)
}

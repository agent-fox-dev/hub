package forge

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/txsvc/apikit"
)

// defaultCallDeadline is how long a call to the forge may take, the retries issuex makes
// inside it included.
const defaultCallDeadline = 30 * time.Second

// callDeadline is the deadline of a call to the forge. It is a variable so that a test can
// shorten it.
var callDeadline = defaultCallDeadline

// maxRedirects is how many redirects of the same host a call follows.
const maxRedirects = 10

// newHTTPClient builds the one HTTP client every forge call goes through. Its base
// transport is base or, when that is nil, a clone of http.DefaultTransport, which honours
// the environment's proxy settings as hub's git transport does. Every request carries hub's
// user agent, and a redirect to another host, port or scheme is refused, so that the token
// goes nowhere but to the host the call was made to.
func newHTTPClient(base http.RoundTripper) *http.Client {
	if base == nil {
		base = http.DefaultTransport
		if t, ok := base.(*http.Transport); ok {
			base = t.Clone()
		}
	}
	return &http.Client{Transport: forgeTransport{base: base}, CheckRedirect: refuseCrossHost}
}

// forgeTransport sets what hub decides about every request to a forge.
type forgeTransport struct{ base http.RoundTripper }

// RoundTrip sends a copy of the request with hub's user agent in place of whichever one
// the client chose, and with no token header when the call is made for a workspace that has
// no credential: issuex falls back to the token of the hub's own environment (GITHUB_TOKEN
// and the like) when it is given none, and that one is not the workspace's.
func (t forgeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	out.Header.Set("User-Agent", "af-hub/"+apikit.Version)
	if token, ok := req.Context().Value(credentialKey{}).(string); ok && token == "" {
		out.Header.Del("Authorization")
		out.Header.Del("PRIVATE-TOKEN")
	}
	return t.base.RoundTrip(out)
}

// credentialKey is the key of the context value that tells forgeTransport which token the
// workspace has.
type credentialKey struct{}

// withCredential returns a context for calls made with the workspace's token, which is
// empty for a workspace without a credential.
func withCredential(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, credentialKey{}, token)
}

// refuseCrossHost is the client's redirect policy. It follows a redirect to the same host,
// port and scheme, up to maxRedirects of them, and refuses any other: GitLab's PRIVATE-TOKEN
// is not a header net/http removes when it follows a redirect to another host, and a
// redirect to plain http would send the token in the clear.
func refuseCrossHost(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("%w: stopped after %d redirects", errRedirectRefused, maxRedirects)
	}
	first := via[0].URL
	if !strings.EqualFold(req.URL.Host, first.Host) || !strings.EqualFold(req.URL.Scheme, first.Scheme) {
		return errRedirectRefused
	}
	return nil
}

package gitserver

import (
	"net/http"
	"strings"
	"testing"
)

// TS-06-26: A valid info/refs GET request causes the handler to create a
// session, call AdvertisedReferences(), encode the result as pkt-line,
// and write it to the HTTP response with the correct Content-Type.
// Requirement: 06-REQ-8.1

// TestBridge_InfoRefs_UploadPack_PktLineRefAdvertisement verifies that
// GET info/refs?service=git-upload-pack returns a well-formed pkt-line
// ref advertisement.
func TestBridge_InfoRefs_UploadPack_PktLineRefAdvertisement(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")

	// Create a real git repository with at least one ref.
	env.initWorkspaceRepo(t, "myws")

	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-upload-pack", "",
		withBasicAuth("x-token-auth", "af_key_user1"))

	// Should return HTTP 200.
	if rec.Code != http.StatusOK {
		t.Errorf("GET info/refs?service=git-upload-pack: status = %d; want %d",
			rec.Code, http.StatusOK)
	}

	// Content-Type must match the git smart HTTP specification.
	ct := rec.Header().Get("Content-Type")
	expectedCT := "application/x-git-upload-pack-advertisement"
	if ct != expectedCT {
		t.Errorf("Content-Type = %q; want %q", ct, expectedCT)
	}

	body := rec.Body.String()

	// The pkt-line response MUST contain the service announcement line.
	if !strings.Contains(body, "# service=git-upload-pack") {
		t.Errorf("response body missing service announcement '# service=git-upload-pack'; got %q",
			truncate(body, 300))
	}

	// The body must contain at least one ref line (the repo has at least HEAD/master).
	// Refs are 40-char hex SHAs followed by a space and ref name.
	hasRef := false
	for _, line := range strings.Split(body, "\n") {
		// A ref line contains a 40-char SHA.
		if len(line) > 44 && isHexString(line[4:44]) {
			hasRef = true
			break
		}
	}
	if !hasRef {
		t.Errorf("response body does not contain any ref lines; got %q",
			truncate(body, 300))
	}

	// The body must end with a flush packet (0000).
	if !strings.Contains(body, "0000") {
		t.Error("response body does not contain flush packet (0000)")
	}
}

// TestBridge_InfoRefs_ReceivePack_PktLineRefAdvertisement verifies that
// GET info/refs?service=git-receive-pack returns a well-formed pkt-line
// ref advertisement.
// Requirement: 06-REQ-8.1
func TestBridge_InfoRefs_ReceivePack_PktLineRefAdvertisement(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-receive-pack", "",
		withBasicAuth("x-token-auth", "af_key_user1"))

	if rec.Code != http.StatusOK {
		t.Errorf("GET info/refs?service=git-receive-pack: status = %d; want %d",
			rec.Code, http.StatusOK)
	}

	ct := rec.Header().Get("Content-Type")
	expectedCT := "application/x-git-receive-pack-advertisement"
	if ct != expectedCT {
		t.Errorf("Content-Type = %q; want %q", ct, expectedCT)
	}

	body := rec.Body.String()

	if !strings.Contains(body, "# service=git-receive-pack") {
		t.Errorf("response body missing service announcement '# service=git-receive-pack'; got %q",
			truncate(body, 300))
	}

	if !strings.Contains(body, "0000") {
		t.Error("response body does not contain flush packet (0000)")
	}
}

// TS-06-27: A valid POST request to git-upload-pack streams the request
// body to the session and streams the session output back as the HTTP
// response body.
// Requirement: 06-REQ-8.2

// TestBridge_PostUploadPack_StreamsPackResponse verifies that POST to
// git-upload-pack returns HTTP 200 with a streaming pack response
// containing PACK data.
func TestBridge_PostUploadPack_StreamsPackResponse(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	// First, get the refs to find a SHA to request.
	refsRec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-upload-pack", "",
		withBasicAuth("x-token-auth", "af_key_user1"))

	if refsRec.Code != http.StatusOK {
		t.Fatalf("info/refs: status = %d; want %d", refsRec.Code, http.StatusOK)
	}

	// Build a minimal upload-pack negotiation request.
	// The want line references the HEAD SHA followed by capabilities.
	// For the test, we send a minimal request and verify the response format.
	wantSHA := extractFirstSHA(refsRec.Body.String())
	if wantSHA == "" {
		t.Fatal("could not extract SHA from info/refs response")
	}

	// Construct a minimal upload-pack request in pkt-line format.
	wantLine := "want " + wantSHA + "\n"
	uploadPackReq := pktLine(wantLine) + "00000009done\n"

	rec := env.doRequest(t, http.MethodPost,
		"/git/myorg/myws.git/git-upload-pack", uploadPackReq,
		withBasicAuth("x-token-auth", "af_key_user1"))

	if rec.Code != http.StatusOK {
		t.Errorf("POST git-upload-pack: status = %d; want %d",
			rec.Code, http.StatusOK)
	}

	ct := rec.Header().Get("Content-Type")
	expectedCT := "application/x-git-upload-pack-result"
	if ct != expectedCT {
		t.Errorf("Content-Type = %q; want %q", ct, expectedCT)
	}

	// The response should contain PACK magic bytes (0x5041434b = "PACK").
	body := rec.Body.String()
	if !strings.Contains(body, "PACK") {
		t.Errorf("response body does not contain PACK magic bytes; got %q",
			truncate(body, 200))
	}
}

// TestBridge_PostReceivePack_ContentType verifies that POST to
// git-receive-pack returns the correct Content-Type.
// Requirement: 06-REQ-8.2
func TestBridge_PostReceivePack_ContentType(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	rec := env.doRequest(t, http.MethodPost,
		"/git/myorg/myws.git/git-receive-pack", "",
		withBasicAuth("x-token-auth", "af_key_user1"))

	if rec.Code != http.StatusOK {
		t.Errorf("POST git-receive-pack: status = %d; want %d",
			rec.Code, http.StatusOK)
	}

	ct := rec.Header().Get("Content-Type")
	expectedCT := "application/x-git-receive-pack-result"
	if ct != expectedCT {
		t.Errorf("Content-Type = %q; want %q", ct, expectedCT)
	}
}

// TS-06-28: When the go-git session returns an error during streaming,
// the handler encodes the error in pkt-line ERR format and writes it to
// the response body if headers have not yet been sent.
// Requirement: 06-REQ-8.3

// TestBridge_SessionError_PktLineERR verifies that when the session
// encounters an error, the response contains a pkt-line ERR message.
func TestBridge_SessionError_PktLineERR(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	// Send a malformed upload-pack request body to trigger a session error.
	rec := env.doRequest(t, http.MethodPost,
		"/git/myorg/myws.git/git-upload-pack", "INVALID_PROTOCOL_DATA",
		withBasicAuth("x-token-auth", "af_key_user1"))

	// Per git smart HTTP spec, session errors return HTTP 200 with a
	// pkt-line ERR body (not an HTTP error status code).
	if rec.Code != http.StatusOK {
		t.Errorf("POST git-upload-pack with invalid body: status = %d; want %d",
			rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "ERR") {
		t.Errorf("response body should contain pkt-line 'ERR' message; got %q",
			truncate(body, 200))
	}
}

// TestBridge_EmptyPostBody_PktLineERR verifies that an empty request body
// for a POST pack operation triggers a session error encoded as pkt-line ERR.
// Requirement: 06-REQ-8.E3
func TestBridge_EmptyPostBody_PktLineERR(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	// Send an empty body to git-upload-pack.
	rec := env.doRequest(t, http.MethodPost,
		"/git/myorg/myws.git/git-upload-pack", "",
		withBasicAuth("x-token-auth", "af_key_user1"))

	// The session should produce an error for the empty request.
	if rec.Code != http.StatusOK {
		t.Errorf("POST git-upload-pack with empty body: status = %d; want %d",
			rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "ERR") {
		t.Errorf("response body should contain pkt-line 'ERR' for empty body; got %q",
			truncate(body, 200))
	}
}

// TestBridge_InvalidService_Returns403 verifies that an invalid service
// query parameter on info/refs returns HTTP 403.
// Requirement: 06-REQ-1.E1
func TestBridge_InvalidService_Returns403(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-foo-bar", "",
		withBasicAuth("x-token-auth", "af_key_user1"))

	if rec.Code != http.StatusForbidden {
		t.Errorf("GET info/refs with invalid service: status = %d; want %d",
			rec.Code, http.StatusForbidden)
	}
}

// --- Bridge test helpers ---

// isHexString returns true if s consists entirely of hex characters [0-9a-f].
func isHexString(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return len(s) > 0
}

// extractFirstSHA finds the first 40-character hex SHA in a pkt-line response.
// Requires the SHA to be followed by a space (ref name separator) to avoid
// false matches against pkt-line length prefixes which are also hex.
func extractFirstSHA(body string) string {
	for i := 0; i+41 <= len(body); i++ {
		candidate := body[i : i+40]
		if body[i+40] == ' ' && isHexString(candidate) {
			return candidate
		}
	}
	return ""
}

// pktLine encodes a string in git pkt-line format (4-hex-digit length prefix).
func pktLine(s string) string {
	// pkt-line length includes the 4-byte length prefix itself.
	length := len(s) + 4
	return strings.Replace(
		strings.Replace(
			strings.Replace(
				strings.Replace(
					pktLineHex(length), "", "", 0,
				), "", "", 0,
			), "", "", 0,
		), "", "", 0,
	) + s
}

// pktLineHex formats an integer as a 4-character hex string for pkt-line.
func pktLineHex(n int) string {
	const hexDigits = "0123456789abcdef"
	return string([]byte{
		hexDigits[(n>>12)&0xf],
		hexDigits[(n>>8)&0xf],
		hexDigits[(n>>4)&0xf],
		hexDigits[n&0xf],
	})
}

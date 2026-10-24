package forge

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/agent-fox-dev/aflib/issuex"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/agent-fox-dev/hub/internal/secrets"
)

// The workspace settings that override the API URL of the forge, one for each
// target. The upstream one has no fallback to the origin one: an upstream on another
// host must not inherit the fork's API URL.
const (
	originAPIURLVariable   = "FORGE_API_URL"
	upstreamAPIURLVariable = "UPSTREAM_FORGE_API_URL"
)

// patUsername is the username under which hub stores a personal access token as a
// git credential (GIT_PAT).
const patUsername = "x-token-auth"

// credentialKind is how hub authenticates to the forge.
type credentialKind string

const (
	// credentialPAT is a basic-auth result whose username is x-token-auth.
	credentialPAT credentialKind = "pat"
	// credentialBasic is any other username; the password is sent as the token.
	credentialBasic credentialKind = "basic"
	// credentialNone is no credential: the client is unauthenticated.
	credentialNone credentialKind = "none"
)

// credentialOf reads the kind of a credential and the token it stands for from the
// result of a resolver.
func credentialOf(auth transport.AuthMethod) (credentialKind, string) {
	basic, ok := auth.(*githttp.BasicAuth)
	if !ok || basic == nil {
		return credentialNone, ""
	}
	if basic.Username == patUsername {
		return credentialPAT, basic.Password
	}
	return credentialBasic, basic.Password
}

// forgeClient is an issuex client built for one request, with what the routes need
// to know of how it was built.
type forgeClient struct {
	api    issuex.Client
	repo   issuex.Repo
	kind   credentialKind
	token  string
	target target

	// forge is the forge the client talks to, which the body limit of a write depends on.
	forge issuex.ForgeType
	// root is the API root of a client built for a write, for the requests hub sends itself.
	root string
}

// buildClient builds a fresh issuex client for the request's workspace and target,
// from what hub holds for it and nothing the request carries but the target. Nothing
// is cached, so a rotated secret or a changed setting applies to the next call.
//
// A client for a write is given the API URL explicitly, the setting's or the one hub
// states for the host, where a client for a read finds it by itself (and honours
// GITHUB_API_URL and GITLAB_API_URL of the hub's environment). A write sends some of its
// requests itself, beside the ones issuex sends, and the two must go to the same place.
func (h *handlers) buildClient(req *request, tgt target, forWrite bool) (*forgeClient, error) {
	remote, variable, resolve := req.remotes.gitURL, originAPIURLVariable, h.cfg.ResolveOriginAuth
	if tgt == targetUpstream {
		remote, variable, resolve = req.remotes.upstreamURL, upstreamAPIURLVariable, h.cfg.ResolveUpstreamAuth
	}

	repo, ok := parseRemote(remote)
	if !ok {
		return nil, &forgeError{
			status:    http.StatusUnprocessableEntity,
			errorType: "forge_unknown",
			message:   fmt.Sprintf("cannot read a forge repository from the remote URL %q (expected https://<host>/<owner>/<name>)", shownRemote(remote)),
		}
	}

	baseURL, err := h.apiURL(req.slug, tgt, variable)
	if err != nil {
		return nil, err
	}

	auth, err := resolve(req.slug)
	if err != nil {
		return nil, storeFailure("reading the credential", req.slug, tgt, err)
	}
	kind, token := credentialOf(auth)

	opts := issuex.Options{
		BaseURL:    baseURL,
		Token:      token,
		RemoteURL:  redactRemote(remote),
		Repo:       repo,
		HTTPClient: h.httpClient,
	}
	forge, err := issuex.DetectForge(opts)
	if err == nil && forWrite && opts.BaseURL == "" {
		opts.BaseURL = defaultAPIURL(forge, repo.Host)
		forge, err = issuex.DetectForge(opts)
	}
	if errors.Is(err, issuex.ErrAmbiguousForge) {
		return nil, ambiguousForge(repo.Host, variable, baseURL != "")
	}
	if err != nil {
		slog.Error("forge: telling the forge failed", "slug", req.slug, "target", string(tgt), "error", err)
		return nil, errInternal()
	}
	api, err := h.cfg.NewClient(opts)
	if err != nil {
		slog.Error("forge: building the client failed", "slug", req.slug, "target", string(tgt), "error", err)
		return nil, errInternal()
	}
	return &forgeClient{api: api, repo: repo, forge: forge, kind: kind, token: token, target: tgt, root: apiRoot(forge, opts.BaseURL)}, nil
}

// apiRoot is the root of the forge's API as a client given base reaches it: without a
// trailing slash, and with /api/v4 after a GitLab URL that lacks it, which issuex adds.
func apiRoot(forge issuex.ForgeType, base string) string {
	root := strings.TrimRight(strings.TrimSpace(base), "/")
	if forge == issuex.ForgeTypeGitLab && !strings.HasSuffix(root, "/api/v4") {
		root += "/api/v4"
	}
	return root
}

// apiURL reads the workspace variable that overrides the forge's API URL. It returns
// "" when the variable is not set, in which case issuex finds the API from the host.
// A value that is set must be a valid API URL.
func (h *handlers) apiURL(slug string, tgt target, variable string) (string, error) {
	value, err := h.cfg.GetVariable("workspace", slug, variable)
	var notFound *secrets.NotFoundError
	if errors.As(err, &notFound) {
		return "", nil
	}
	if err != nil {
		return "", storeFailure("reading "+variable, slug, tgt, err)
	}
	value = strings.TrimSpace(value)
	if !validAPIURL(value) {
		return "", &forgeError{
			status:    http.StatusUnprocessableEntity,
			errorType: "forge_api_url_invalid",
			message:   variable + " must be an absolute https:// URL with a host and without userinfo, query or fragment",
		}
	}
	return value, nil
}

// storeFailure logs a failure of the secrets or variables store and answers it 500.
// The error is not logged as text: it may carry what the store held, and the log
// must not hold a secret. Its type says enough to tell a store failure from another.
func storeFailure(what, slug string, tgt target, err error) *forgeError {
	slog.Error("forge: "+what+" failed", "slug", slug, "target", string(tgt), "error_type", fmt.Sprintf("%T", err))
	return errInternal()
}

// ambiguousForge is the answer when neither the host nor the API URL says which
// forge serves the repository.
func ambiguousForge(host, variable string, apiURLSet bool) *forgeError {
	msg := fmt.Sprintf("cannot tell which forge serves %s; set %s to its API URL, an https URL that contains github or gitlab", host, variable)
	if apiURLSet {
		msg = fmt.Sprintf("cannot tell which forge serves %s: neither the host nor the API URL in %s names exactly one of github and gitlab", host, variable)
	}
	return &forgeError{status: http.StatusUnprocessableEntity, errorType: "forge_unknown", message: msg}
}

// validAPIURL reports whether value is an absolute https URL with a host and no
// userinfo, query, fragment or whitespace.
func validAPIURL(value string) bool {
	if value == "" || strings.ContainsFunc(value, func(r rune) bool { return r <= ' ' || r == 0x7f }) || strings.ContainsAny(value, "?#") {
		return false
	}
	u, err := url.Parse(value)
	if err != nil {
		return false
	}
	return u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Opaque == ""
}

// parseRemote reads the repository of a remote URL. issuex's parser reads the hosts
// it knows as forges; any other host is read by splitting the URL, so that the
// repository can be named when issuex cannot tell which forge serves it, and so that
// an API URL that names the forge can decide.
func parseRemote(remote string) (issuex.Repo, bool) {
	if repo, ok := issuex.ParseRemote(remote); ok {
		return repo, true
	}
	return splitRemote(remote)
}

// splitRemote splits an https, http, ssh or git URL, or an scp-style remote, into
// host, owner and name, whatever the host.
func splitRemote(remote string) (issuex.Repo, bool) {
	s := strings.TrimSpace(remote)
	var host, path string
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return issuex.Repo{}, false
		}
		switch strings.ToLower(u.Scheme) {
		case "https", "http", "ssh", "git":
		default:
			return issuex.Repo{}, false
		}
		host, path = u.Hostname(), u.Path
	} else if userHost, repoPath, found := strings.Cut(s, ":"); found {
		if at := strings.LastIndex(userHost, "@"); at >= 0 {
			userHost = userHost[at+1:]
		}
		host, path = userHost, repoPath
	} else {
		return issuex.Repo{}, false
	}
	host = strings.ToLower(strings.TrimPrefix(host, "www."))
	if host == "" {
		return issuex.Repo{}, false
	}
	repo, ok := issuex.ParseRepo(strings.Trim(path, "/"))
	if !ok {
		return issuex.Repo{}, false
	}
	repo.Host = host
	return repo, true
}

// urlUserinfo matches the scheme and userinfo of a URL, up to the last @ of its
// authority.
var urlUserinfo = regexp.MustCompile(`(?i)^([a-z][a-z0-9+.\-]*://)[^/\s?#]*@`)

// redactRemote removes the userinfo of a remote URL, in the URL form and in the
// scp-style form, so that the URL can be shown and logged.
func redactRemote(remote string) string {
	if strings.Contains(remote, "://") {
		return urlUserinfo.ReplaceAllString(remote, "$1")
	}
	head := remote
	if i := strings.IndexByte(head, '/'); i >= 0 {
		head = head[:i]
	}
	if at := strings.LastIndexByte(head, '@'); at >= 0 {
		return remote[at+1:]
	}
	return remote
}

// shownRemote is a remote URL for a message. It is redactRemote's and, when a @ is left in
// it, only what follows the last one: a password may hold a character that ends the
// authority of a URL (/, ? or #), and then the pattern of redactRemote does not reach the
// @ that closes the user information.
func shownRemote(remote string) string {
	shown := redactRemote(remote)
	if at := strings.LastIndexByte(shown, '@'); at >= 0 {
		return shown[at+1:]
	}
	return shown
}

// remoteInfo describes how hub reaches the forge of one remote, without contacting it.
type remoteInfo struct {
	// Forge is github, gitlab or unknown.
	Forge string `json:"forge"`
	// APIURL is the API the forge is reached at; null when the forge is unknown.
	APIURL *string `json:"api_url"`
	// Repository is the repository the remote URL names; null when it names none.
	Repository *repositoryInfo `json:"repository"`
	// Credential is the kind of the credential, pat, basic or none, never its value.
	Credential credentialKind `json:"credential"`
}

// repositoryInfo is a repository as the diagnostic shows it. Owner carries the full
// namespace path, for example group/subgroup on GitLab.
type repositoryInfo struct {
	Host  string `json:"host"`
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

// describeRemote reports how a read would reach the forge of the request's workspace
// for the target, without sending a request. A forge that cannot be classified, and a
// remote that names no repository, are reported as unknown and are no error; an
// invalid API URL variable and a failure of the store are.
func (h *handlers) describeRemote(req *request, tgt target) (*remoteInfo, error) {
	remote, variable, resolve := req.remotes.gitURL, originAPIURLVariable, h.cfg.ResolveOriginAuth
	if tgt == targetUpstream {
		remote, variable, resolve = req.remotes.upstreamURL, upstreamAPIURLVariable, h.cfg.ResolveUpstreamAuth
	}

	baseURL, err := h.apiURL(req.slug, tgt, variable)
	if err != nil {
		return nil, err
	}
	auth, err := resolve(req.slug)
	if err != nil {
		return nil, storeFailure("reading the credential", req.slug, tgt, err)
	}
	kind, _ := credentialOf(auth)

	info := &remoteInfo{Forge: "unknown", Credential: kind}
	repo, ok := parseRemote(remote)
	if !ok {
		return info, nil
	}
	info.Repository = &repositoryInfo{Host: repo.Host, Owner: repo.Owner, Name: repo.Name}

	forge, err := issuex.DetectForge(issuex.Options{BaseURL: baseURL, RemoteURL: redactRemote(remote), Repo: repo})
	if err != nil || (forge != issuex.ForgeTypeGitHub && forge != issuex.ForgeTypeGitLab) {
		return info, nil
	}
	info.Forge = string(forge)
	apiURL := strings.TrimRight(baseURL, "/")
	switch {
	case apiURL == "":
		apiURL = defaultAPIURL(forge, repo.Host)
	case forge == issuex.ForgeTypeGitLab && !strings.HasSuffix(apiURL, "/api/v4"):
		// issuex puts /api/v4 after a GitLab API URL that does not end with it.
		apiURL += "/api/v4"
	}
	info.APIURL = &apiURL
	return info, nil
}

// defaultAPIURL is the API URL issuex reaches a forge at when no variable names one:
// the public API of github.com, and /api/v3 or /api/v4 on the host of a self-hosted
// GitHub or GitLab. issuex has no accessor for it.
func defaultAPIURL(forge issuex.ForgeType, host string) string {
	if forge == issuex.ForgeTypeGitLab {
		return "https://" + host + "/api/v4"
	}
	if host == "github.com" {
		return "https://api.github.com"
	}
	return "https://" + host + "/api/v3"
}

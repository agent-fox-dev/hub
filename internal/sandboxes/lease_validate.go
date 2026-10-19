package sandboxes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Limits on what a caller may put in a POST body.
const (
	minLeaseTTL      = time.Minute
	maxPurposeRunes  = 64
	maxLabels        = 16
	maxLabelValueLen = 256
)

// labelKeyShape is a label key: 1 to 63 characters from [A-Za-z0-9._-] that
// starts and ends with a letter or a digit.
var labelKeyShape = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]{0,61}[A-Za-z0-9])?$`)

// issueRequest is the body of POST /workspaces/:slug/sandboxes. A caller says
// which profile, never what is in it, so any other field is refused. Profile
// and TTL are pointers to tell an absent field from an empty one; labels stay
// untyped so that a value that is not a string can be named.
type issueRequest struct {
	Profile *string        `json:"profile"`
	Purpose string         `json:"purpose"`
	TTL     *string        `json:"ttl"`
	Labels  map[string]any `json:"labels"`
}

// requestError is a request the caller got wrong. It answers 400 with its
// message.
type requestError struct{ msg string }

func (e *requestError) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return &requestError{msg: fmt.Sprintf(format, args...)}
}

// asRequestError reports whether err is a request the caller got wrong.
func asRequestError(err error) (*requestError, bool) {
	var re *requestError
	return re, errors.As(err, &re)
}

// parseIssueRequest decodes the request body, which must be a JSON object with
// no field but profile, purpose, ttl and labels. An empty body counts as {}.
func parseIssueRequest(body io.Reader) (issueRequest, error) {
	var req issueRequest
	raw, err := io.ReadAll(body)
	if err != nil {
		return req, err
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return req, nil
	}
	if raw[0] != '{' {
		return req, badRequest("request body must be a JSON object")
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, decodeError(err)
	}
	if dec.More() {
		return req, badRequest("request body must be a single JSON object")
	}
	return req, nil
}

// decodeError turns a decoding failure into the request error that names the
// field at fault.
func decodeError(err error) error {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return badRequest("field %q must be a %s, not a %s", typeErr.Field, jsonKind(typeErr.Type.String()), typeErr.Value)
	}
	if field, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		return badRequest("unknown field %s: only profile, purpose, ttl and labels are accepted", field)
	}
	return badRequest("request body is not valid JSON")
}

// jsonKind names a Go type the way JSON does.
func jsonKind(goType string) string {
	switch {
	case strings.HasPrefix(goType, "map["):
		return "object"
	case goType == "string":
		return "string"
	default:
		return goType
	}
}

// newIssueParams validates a request and resolves it into the profile, TTL,
// purpose and labels of a lease. A profile is named exactly or, when the
// request names none, is the default one; the TTL is the profile's unless the
// request asks for less, but never under a minute.
func (s *LeaseService) newIssueParams(req issueRequest) (issueParams, error) {
	profile, err := s.resolveProfile(req.Profile)
	if err != nil {
		return issueParams{}, err
	}
	ttl, err := resolveTTL(req.TTL, profile)
	if err != nil {
		return issueParams{}, err
	}
	if err := validatePurpose(req.Purpose); err != nil {
		return issueParams{}, err
	}
	labels, err := validateLabels(req.Labels)
	if err != nil {
		return issueParams{}, err
	}
	return issueParams{profile: profile, purpose: req.Purpose, ttl: ttl, labels: labels}, nil
}

// profileNames lists the configured profile names in configuration order.
func (s *LeaseService) profileNames() string {
	var names []string
	for _, p := range s.profiles.Profiles() {
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}

// resolveProfile returns the profile named by name (exact and case-sensitive),
// or the default profile when name is absent.
func (s *LeaseService) resolveProfile(name *string) (ValidatedProfile, error) {
	if name == nil {
		if p, ok := s.defaultProfile(); ok {
			return p, nil
		}
		return ValidatedProfile{}, badRequest("profile is required: no profile is marked as the default; known profiles: %s", s.profileNames())
	}
	if p, ok := s.profileByName(*name); ok {
		return p, nil
	}
	return ValidatedProfile{}, badRequest("unknown sandbox profile %q; known profiles: %s", *name, s.profileNames())
}

// resolveTTL returns the requested TTL, or the profile's when none is requested.
func resolveTTL(requested *string, profile ValidatedProfile) (time.Duration, error) {
	if requested == nil {
		return profile.TTL, nil
	}
	ttl, err := time.ParseDuration(*requested)
	if err != nil {
		return 0, badRequest("ttl %q is not a valid duration such as 30m or 1h", *requested)
	}
	if ttl < minLeaseTTL {
		return 0, badRequest("ttl %q is shorter than the minimum of %s", *requested, shortDuration(minLeaseTTL))
	}
	if ttl > profile.TTL {
		return 0, badRequest("ttl %q is longer than the ttl of profile %q, %s", *requested, profile.Name, shortDuration(profile.TTL))
	}
	return ttl, nil
}

// shortDuration formats d without the zero units Duration.String adds: 1h, not
// 1h0m0s.
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-2]
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2]
	}
	return s
}

// hasControl reports whether s holds a control character.
func hasControl(s string) bool {
	return strings.ContainsFunc(s, unicode.IsControl)
}

// validatePurpose accepts at most 64 characters (runes) and no control
// character.
func validatePurpose(purpose string) error {
	if utf8.RuneCountInString(purpose) > maxPurposeRunes {
		return badRequest("purpose is longer than %d characters", maxPurposeRunes)
	}
	if hasControl(purpose) {
		return badRequest("purpose must not contain control characters")
	}
	return nil
}

// validateLabels accepts at most 16 pairs of strings. A key has 1 to 63
// characters from [A-Za-z0-9._-], starts and ends with a letter or a digit, and
// is not "lease" or "workspace" in any case, which hub's own labels use. A value
// has at most 256 bytes and no control character. The first violation, in key
// order, is reported with its key; for too many labels it is the first key
// past the sixteenth.
func validateLabels(raw map[string]any) (map[string]string, error) {
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	labels := make(map[string]string, len(raw))
	for i, k := range keys {
		switch {
		case i >= maxLabels:
			return nil, badRequest("label %q is over the limit of %d labels", k, maxLabels)
		case !labelKeyShape.MatchString(k):
			return nil, badRequest("label key %q is not 1 to 63 characters of letters, digits, '.', '_' and '-', starting and ending with a letter or digit", k)
		case strings.EqualFold(k, "lease") || strings.EqualFold(k, "workspace"):
			return nil, badRequest("label key %q is reserved", k)
		}
		v, ok := raw[k].(string)
		switch {
		case !ok:
			return nil, badRequest("value of label %q must be a string", k)
		case len(v) > maxLabelValueLen:
			return nil, badRequest("value of label %q is longer than %d bytes", k, maxLabelValueLen)
		case hasControl(v):
			return nil, badRequest("value of label %q must not contain control characters", k)
		}
		labels[k] = v
	}
	return labels, nil
}

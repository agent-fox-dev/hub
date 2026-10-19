package sandboxes

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"
)

// issueRequest is the body of POST /workspaces/:slug/sandboxes.
type issueRequest struct {
	Profile string            `json:"profile"`
	Purpose string            `json:"purpose"`
	TTL     string            `json:"ttl"`
	Labels  map[string]string `json:"labels"`
}

// requestError is a request the caller got wrong. It answers 400 with its
// message.
type requestError struct{ msg string }

func (e *requestError) Error() string { return e.msg }

// parseIssueRequest decodes the request body. An empty body counts as {}.
func parseIssueRequest(body io.Reader) (issueRequest, error) {
	var req issueRequest
	raw, err := io.ReadAll(body)
	if err != nil {
		return req, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return req, nil
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return req, &requestError{"request body must be a JSON object"}
	}
	return req, nil
}

// newIssueParams resolves a request into the profile, TTL, purpose and labels
// of a lease: the named profile or the default one, and the profile's TTL when
// the request gives none.
func (s *LeaseService) newIssueParams(req issueRequest) (issueParams, error) {
	var profile ValidatedProfile
	var ok bool
	if req.Profile == "" {
		profile, ok = s.defaultProfile()
	} else {
		profile, ok = s.profileByName(req.Profile)
	}
	if !ok {
		return issueParams{}, &requestError{"unknown sandbox profile"}
	}

	ttl := profile.TTL
	if req.TTL != "" {
		d, err := time.ParseDuration(req.TTL)
		if err != nil {
			return issueParams{}, &requestError{"ttl is not a valid duration"}
		}
		ttl = d
	}
	return issueParams{profile: profile, purpose: req.Purpose, ttl: ttl, labels: req.Labels}, nil
}

// asRequestError reports whether err is a request the caller got wrong.
func asRequestError(err error) (*requestError, bool) {
	var re *requestError
	return re, errors.As(err, &re)
}

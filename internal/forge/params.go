package forge

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/agent-fox-dev/aflib/issuex"
)

// The limit of GET /issues: the number of issues to return, at most a page of the
// forges.
const (
	defaultLimit = 30
	maxLimit     = 100
)

// badRequest is the refusal of a parameter, which names the parameter first.
func badRequest(format string, args ...any) *refusal {
	return &refusal{status: http.StatusBadRequest, message: fmt.Sprintf(format, args...)}
}

// positiveInt reads a string of decimal digits, leading zeros included, as a number
// greater than zero. A sign, a point, a letter or a value that does not fit an int
// is not one.
func positiveInt(s string) (int, bool) {
	if s == "" || strings.ContainsFunc(s, func(r rune) bool { return r < '0' || r > '9' }) {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 0
}

// parseNumber reads the :n path parameter, the number of an issue or pull request.
// On GitLab it is the project's iid, as issuex has it.
func parseNumber(value string) (int, *refusal) {
	n, ok := positiveInt(value)
	if !ok {
		return 0, badRequest("n must be a positive decimal integer")
	}
	return n, nil
}

// parseIssueFilter reads the query of GET /issues. A parameter that is absent is left
// out of the filter, for issuex to default; the limit is the exception and is always
// set. Empty labels are dropped.
func parseIssueFilter(q url.Values) (issuex.IssueFilter, *refusal) {
	var (
		f   issuex.IssueFilter
		ref *refusal
	)
	if f.State, ref = oneOf(q, "state", "open", "closed", "all"); ref != nil {
		return f, ref
	}
	if f.Sort, ref = oneOf(q, "sort", "created", "updated"); ref != nil {
		return f, ref
	}
	if f.Direction, ref = oneOf(q, "direction", "asc", "desc"); ref != nil {
		return f, ref
	}
	for _, value := range q["labels"] {
		for _, label := range strings.Split(value, ",") {
			if label = strings.TrimSpace(label); label != "" {
				f.Labels = append(f.Labels, label)
			}
		}
	}
	f.Assignee = q.Get("assignee")

	f.Limit, ref = parseLimit(q)
	return f, ref
}

// parseLimit reads the limit parameter of a list: an integer from 1 to maxLimit, given
// once, or defaultLimit when it is absent.
func parseLimit(q url.Values) (int, *refusal) {
	values, present := q["limit"]
	if !present {
		return defaultLimit, nil
	}
	if len(values) != 1 {
		return 0, badRequest("limit must be given once")
	}
	n, ok := positiveInt(values[0])
	if !ok || n > maxLimit {
		return 0, badRequest("limit must be an integer from 1 to %d", maxLimit)
	}
	return n, nil
}

// oneOf reads a parameter that is one of the allowed values. It returns "" when the
// parameter is absent; an empty, repeated or other value is refused.
func oneOf(q url.Values, name string, allowed ...string) (string, *refusal) {
	values, present := q[name]
	if !present {
		return "", nil
	}
	if len(values) != 1 || !slices.Contains(allowed, values[0]) {
		return "", badRequest("%s must be one of: %s", name, strings.Join(allowed, ", "))
	}
	return values[0], nil
}

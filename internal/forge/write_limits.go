package forge

import (
	"fmt"
	"net/http"
	"unicode/utf8"

	"github.com/agent-fox-dev/aflib/issuex"
)

// The largest body-like field, in Unicode code points, a forge accepts. GitHub's is its
// limit on an issue, a pull request and a comment. GitLab's is the smaller of its limits
// on a note and on a description, so that one value is never above either.
const (
	githubBodyLimit = 65536
	gitlabBodyLimit = 1000000
)

// textField is a body-like field of a write, named as the request names it.
type textField struct {
	name, text string
}

// bodyLimit is the limit of the forge. A forge that is neither gets GitHub's, the smaller.
func bodyLimit(forge issuex.ForgeType) int {
	if forge == issuex.ForgeTypeGitLab {
		return gitlabBodyLimit
	}
	return githubBodyLimit
}

// checkBodyLimits refuses the first of the fields that is longer than the limit of the
// forge with 413 body_too_large, whose message names the field, its length, the forge
// and the limit. Length is counted in code points, whatever the bytes of each.
func checkBodyLimits(forge issuex.ForgeType, fields []textField) *refusal {
	limit := bodyLimit(forge)
	for _, f := range fields {
		if n := utf8.RuneCountInString(f.text); n > limit {
			return &refusal{
				status:    http.StatusRequestEntityTooLarge,
				errorType: "body_too_large",
				message:   fmt.Sprintf("%s is %d characters; %s accepts at most %d", f.name, n, forge, limit),
			}
		}
	}
	return nil
}

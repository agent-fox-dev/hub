package forge

import (
	"net/url"
	"reflect"
	"testing"

	"github.com/agent-fox-dev/aflib/issuex"
)

// parseNumber accepts a positive decimal integer, leading zeros included, and nothing
// else.
// Requirement: 30-REQ-7.3
func TestParseNumber(t *testing.T) {
	for value, want := range map[string]int{"1": 1, "42": 42, "007": 7, "2147483647": 2147483647} {
		got, ref := parseNumber(value)
		if ref != nil || got != want {
			t.Errorf("parseNumber(%q) = %d, %+v; want %d", value, got, ref, want)
		}
	}
	for _, value := range []string{"", "0", "00", "-1", "+1", "1.5", "abc", "007x", " 1", "1 ", "1e3", "٣", "99999999999999999999999"} {
		got, ref := parseNumber(value)
		if ref == nil {
			t.Errorf("parseNumber(%q) = %d; want a refusal", value, got)
			continue
		}
		if ref.status != 400 || ref.message[:2] != "n " {
			t.Errorf("parseNumber(%q) refusal = %+v; want 400 naming n", value, ref)
		}
	}
}

// parseIssueFilter passes only what the query holds, splits and trims the labels, and
// always passes a limit.
// Requirement: 30-REQ-7.4, 30-REQ-7.5
func TestParseIssueFilter(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  issuex.IssueFilter
	}{
		{"nothing", "", issuex.IssueFilter{Limit: 30}},
		{"trimmed labels", "labels=%20a%20,%20,b", issuex.IssueFilter{Labels: []string{"a", "b"}, Limit: 30}},
		{"scoped labels", "labels=af:triage,priority::high", issuex.IssueFilter{Labels: []string{"af:triage", "priority::high"}, Limit: 30}},
		{"all values", "state=closed&sort=created&direction=desc&assignee=eve&limit=7",
			issuex.IssueFilter{State: "closed", Sort: "created", Direction: "desc", Assignee: "eve", Limit: 7}},
		{"limit bounds", "limit=1", issuex.IssueFilter{Limit: 1}},
		{"other parameters", "target=upstream&x=y", issuex.IssueFilter{Limit: 30}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := url.ParseQuery(tc.query)
			if err != nil {
				t.Fatal(err)
			}
			got, ref := parseIssueFilter(q)
			if ref != nil {
				t.Fatalf("refused: %+v", ref)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("filter = %+v; want %+v", got, tc.want)
			}
		})
	}

	for query, param := range map[string]string{
		"state=x": "state", "sort=x": "sort", "direction=x": "direction", "limit=x": "limit",
		"limit=0": "limit", "limit=101": "limit", "state=open&state=open": "state",
	} {
		q, _ := url.ParseQuery(query)
		_, ref := parseIssueFilter(q)
		if ref == nil || ref.status != 400 || ref.message[:len(param)+1] != param+" " {
			t.Errorf("parseIssueFilter(%q) refusal = %+v; want 400 naming %s", query, ref, param)
		}
	}
}

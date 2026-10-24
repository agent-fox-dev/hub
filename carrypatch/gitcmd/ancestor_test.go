package gitcmd

import (
	"context"
	"errors"
	"testing"
)

// Exit 0 means ancestor, exit 1 means not; anything else (unknown commits,
// empty arguments, a cancelled context) is an error.
func TestIsAncestor(t *testing.T) {
	dir, mainSHA, featureSHA := divergedRepo(t, false)
	baseSHA := runGit(t, dir, "rev-parse", "main~1")
	r := newRunner(t, dir)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name, a, b string
		ctx        context.Context
		want       bool
		wantErr    string // "", "git" or "ctx"
	}{
		{"base precedes main", baseSHA, mainSHA, context.Background(), true, ""},
		{"a commit is its own ancestor", mainSHA, mainSHA, context.Background(), true, ""},
		{"diverged branches", mainSHA, featureSHA, context.Background(), false, ""},
		{"unknown commits", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "cafebabecafebabecafebabecafebabecafebabe", context.Background(), false, "git"},
		{"empty commit", "", mainSHA, context.Background(), false, "git"},
		{"cancelled context", mainSHA, mainSHA, cancelled, false, "ctx"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.IsAncestor(tt.ctx, tt.a, tt.b)
			if got != tt.want {
				t.Errorf("IsAncestor = %v, want %v", got, tt.want)
			}
			switch tt.wantErr {
			case "":
				if err != nil {
					t.Errorf("IsAncestor error: %v", err)
				}
			case "git":
				if ge := asGitError(t, err); ge.ExitCode == 1 {
					t.Error("exit code 1 is 'not an ancestor', not an error")
				}
			case "ctx":
				if !errors.Is(err, context.Canceled) {
					t.Errorf("error = %v, want context.Canceled", err)
				}
			}
		})
	}
}

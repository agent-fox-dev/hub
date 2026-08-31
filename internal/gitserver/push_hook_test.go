package gitserver

import (
	"database/sql"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
)

// TestExtractPushedBranches verifies that extractPushedBranches correctly
// extracts branch names from push commands, strips the refs/heads/ prefix,
// and ignores deletes (zero hash in New).
func TestExtractPushedBranches(t *testing.T) {
	nonZero := plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	tests := []struct {
		name     string
		commands []*packp.Command
		want     []string
	}{
		{
			name:     "nil commands",
			commands: nil,
			want:     nil,
		},
		{
			name:     "empty commands",
			commands: []*packp.Command{},
			want:     nil,
		},
		{
			name: "single branch update",
			commands: []*packp.Command{
				{Name: "refs/heads/feature/my-patch", Old: plumbing.ZeroHash, New: nonZero},
			},
			want: []string{"feature/my-patch"},
		},
		{
			name: "multiple branch updates",
			commands: []*packp.Command{
				{Name: "refs/heads/feature/a", Old: nonZero, New: nonZero},
				{Name: "refs/heads/feature/b", Old: plumbing.ZeroHash, New: nonZero},
			},
			want: []string{"feature/a", "feature/b"},
		},
		{
			name: "delete is excluded",
			commands: []*packp.Command{
				{Name: "refs/heads/feature/deleted", Old: nonZero, New: plumbing.ZeroHash},
			},
			want: nil,
		},
		{
			name: "tag ref is excluded",
			commands: []*packp.Command{
				{Name: "refs/tags/v1.0", Old: plumbing.ZeroHash, New: nonZero},
			},
			want: nil,
		},
		{
			name: "mixed: branch update, branch delete, tag",
			commands: []*packp.Command{
				{Name: "refs/heads/feature/keep", Old: nonZero, New: nonZero},
				{Name: "refs/heads/feature/remove", Old: nonZero, New: plumbing.ZeroHash},
				{Name: "refs/tags/v2.0", Old: plumbing.ZeroHash, New: nonZero},
			},
			want: []string{"feature/keep"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPushedBranches(tt.commands)
			if len(got) != len(tt.want) {
				t.Errorf("extractPushedBranches() = %v; want %v", got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("extractPushedBranches()[%d] = %q; want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestRegisterPostPushHook verifies that RegisterPostPushHook sets and clears
// the global hook variable.
func TestRegisterPostPushHook(t *testing.T) {
	// Save and restore the original hook.
	original := postPushHook
	defer func() { postPushHook = original }()

	// Register a hook.
	RegisterPostPushHook(func(_ *sql.DB, _ string, _ []string) {})
	if postPushHook == nil {
		t.Error("postPushHook should not be nil after RegisterPostPushHook")
	}

	// Clear the hook.
	RegisterPostPushHook(nil)
	if postPushHook != nil {
		t.Error("postPushHook should be nil after RegisterPostPushHook(nil)")
	}
}

package campaign

import (
	"testing"
)

// TS-12-15: Spec branch name is derived deterministically from the spec
// directory name by converting the snake_case suffix of NN_snake_case_name
// to kebab-case.
func TestDeriveSpecBranchName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		specDir  string
		expected string
	}{
		{
			name:     "standard spec directory",
			specDir:  "07_secrets_variables",
			expected: "spec/07-secrets-variables",
		},
		{
			name:     "single-word spec name",
			specDir:  "01_workspace",
			expected: "spec/01-workspace",
		},
		{
			name:     "multi-word spec name",
			specDir:  "12_campaign_execution_scheduler",
			expected: "spec/12-campaign-execution-scheduler",
		},
		{
			name:     "two-digit spec ID",
			specDir:  "10_gitcmd",
			expected: "spec/10-gitcmd",
		},
		{
			name:     "three-word spec name",
			specDir:  "06_git_server",
			expected: "spec/06-git-server",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := DeriveSpecBranchName(tc.specDir)
			if got != tc.expected {
				t.Errorf("DeriveSpecBranchName(%q) = %q; want %q", tc.specDir, got, tc.expected)
			}
		})
	}
}

// Verify branch name does not include campaign ID.
func TestDeriveSpecBranchName_NoCampaignID(t *testing.T) {
	t.Parallel()

	branchName := DeriveSpecBranchName("07_secrets_variables")
	if branchName == "" {
		t.Fatal("DeriveSpecBranchName returned empty string")
	}

	// Branch name format is spec/<id>-<name>, no campaign ID prefix.
	expected := "spec/07-secrets-variables"
	if branchName != expected {
		t.Errorf("branch name = %q; want %q (should not include campaign ID)", branchName, expected)
	}
}

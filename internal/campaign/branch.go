package campaign

import "strings"

// DeriveSpecBranchName derives the git branch name from a spec directory name.
// The spec directory follows the convention NN_snake_case_name (e.g. "07_secrets_variables").
// The branch name is spec/<NN>-<kebab-case-name> (e.g. "spec/07-secrets-variables").
func DeriveSpecBranchName(specDir string) string {
	// Replace underscores with hyphens to convert from snake_case to kebab-case.
	kebab := strings.ReplaceAll(specDir, "_", "-")
	return "spec/" + kebab
}

package gitcmd

import "strings"

// parseConflictFiles scans git merge-tree --write-tree output line-by-line
// for lines starting with "CONFLICT" and extracts file paths.
//
// Parsing rule (delimiter split):
// git merge-tree --write-tree (available since git 2.38) emits conflict
// information to stdout when the merge is not clean. Each conflict line
// follows this format:
//
//	CONFLICT (content): Merge conflict in path/to/file.go
//
// The parser splits on "Merge conflict in " and takes the remainder as the
// file path. If a CONFLICT line does not contain that marker (e.g. a
// rename/delete conflict or an unexpected format from a future git version),
// the last space-delimited token is used as a best-effort file path. If the
// line is empty after trimming, an empty string is included so no CONFLICT
// line is silently dropped (11-REQ-5.E3).
//
// Returns a deduplicated slice of file paths. Returns an empty (non-nil)
// slice if no CONFLICT lines are found.
func parseConflictFiles(output string) []string {
	return extractConflictPaths(output)
}

// parseRebaseConflictFiles extracts conflicting file paths from git rebase
// stdout/stderr output. Git rebase (2.38+) emits CONFLICT lines in the same
// format as merge-tree when it encounters conflicting changes during replay.
//
// Representative sample output from git 2.38+ rebase conflict:
//
//	Rebasing (1/1)
//	CONFLICT (content): Merge conflict in conflict.txt
//	error: could not apply abc1234... feature change
//	hint: Resolve all conflicts manually, mark them as resolved with
//	hint: "git add/rm <conflicted_files>", then run "git rebase --continue".
//	hint: You can instead skip this commit: "git rebase --skip".
//	hint: To abort and get back to the state before "git rebase", run
//	hint: "git rebase --abort".
//
// The parser extracts the file path from "Merge conflict in <path>" on
// CONFLICT-prefixed lines. If a CONFLICT line uses a different format (e.g.
// rename/delete), the last space-delimited token is used as a best-effort
// fallback.
//
// Returns a deduplicated slice of file paths. Returns an empty (non-nil)
// slice if no conflict paths are found.
func parseRebaseConflictFiles(output string) []string {
	return extractConflictPaths(output)
}

// parseConflictFilesWithFallback extracts conflicting file paths from the
// combined stdout/stderr of a failed git command (cherry-pick, merge, etc.)
// using parseRebaseConflictFiles. If no CONFLICT lines are found, it returns
// a single-element slice containing the fallback string "(unresolved conflict)"
// so that ConflictingFiles is never empty when the caller knows a conflict
// occurred (14-REQ-13.E1, 14-PROP-6).
func parseConflictFilesWithFallback(combinedOutput string) []string {
	files := parseRebaseConflictFiles(combinedOutput)
	if len(files) == 0 {
		return []string{"(unresolved conflict)"}
	}
	return files
}

// extractConflictPaths is the shared implementation for parsing CONFLICT
// lines from both merge-tree and rebase output. Both git commands use the
// same "CONFLICT (content): Merge conflict in <path>" line format.
func extractConflictPaths(output string) []string {
	files := make([]string, 0)
	seen := make(map[string]bool)

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "CONFLICT") {
			continue
		}

		var path string

		// Try to extract file path from "Merge conflict in <path>" format.
		const marker = "Merge conflict in "
		if idx := strings.Index(line, marker); idx >= 0 {
			path = strings.TrimSpace(line[idx+len(marker):])
		} else {
			// Best-effort: use the last space-delimited token as the path.
			parts := strings.Fields(line)
			if len(parts) > 0 {
				path = parts[len(parts)-1]
			}
			// If Fields returns empty (line was only whitespace after
			// trimming), path stays "" — included per 11-REQ-5.E3.
		}

		// Deduplicate: skip paths we have already recorded.
		if seen[path] {
			continue
		}
		seen[path] = true
		files = append(files, path)
	}

	return files
}

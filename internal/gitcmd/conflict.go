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

package gitcmd

import (
	"reflect"
	"testing"
)

// CONFLICT lines from merge-tree, rebase, cherry-pick and merge all name the
// file after "Merge conflict in "; other conflict kinds fall back to the last
// token, duplicates collapse, and no CONFLICT line means an empty list.
func TestParseConflictFiles(t *testing.T) {
	tests := []struct {
		name, output string
		want         []string
	}{
		{"content conflict", "Rebasing (1/1)\nCONFLICT (content): Merge conflict in conflict.txt\nerror: could not apply abc1234... feature", []string{"conflict.txt"}},
		{"several files deduplicated", "CONFLICT (content): Merge conflict in a.go\nCONFLICT (content): Merge conflict in b.go\nCONFLICT (add/add): Merge conflict in a.go", []string{"a.go", "b.go"}},
		{"other kinds use the last token", "CONFLICT (rename/delete): x.go deleted in HEAD and renamed to y.go", []string{"y.go"}},
		{"no conflict lines", "Auto-merging a.go\nMerge made by the 'ort' strategy.", []string{}},
		{"empty output", "", []string{}},
	}
	for _, tt := range tests {
		for name, parse := range map[string]func(string) []string{"merge-tree": parseConflictFiles, "rebase": parseRebaseConflictFiles} {
			if got := parse(tt.output); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("%s/%s: got %#v, want %#v", tt.name, name, got, tt.want)
			}
		}
	}
}

// When a conflict is known to have happened but no file could be parsed, the
// list still names one entry.
func TestParseConflictFilesWithFallback(t *testing.T) {
	if got := parseConflictFilesWithFallback("error: could not apply abc"); !reflect.DeepEqual(got, []string{"(unresolved conflict)"}) {
		t.Errorf("without CONFLICT lines: %#v", got)
	}
	if got := parseConflictFilesWithFallback("CONFLICT (content): Merge conflict in a.go"); !reflect.DeepEqual(got, []string{"a.go"}) {
		t.Errorf("with CONFLICT lines: %#v", got)
	}
}

func TestConflictErrors_NameOperationAndFiles(t *testing.T) {
	files := []string{"a.go", "b.go"}
	tests := []struct {
		err  error
		want string
	}{
		{&MergeConflictError{ConflictingFiles: files}, "merge conflict in 2 file(s): a.go, b.go"},
		{&RebaseConflictError{ConflictingFiles: files}, "rebase conflict in 2 file(s): a.go, b.go"},
		{&CherryPickConflictError{ConflictingFiles: files}, "cherry-pick conflict in 2 file(s): a.go, b.go"},
		{&MergeNoFFConflictError{ConflictingFiles: files}, "merge --no-ff conflict in 2 file(s): a.go, b.go"},
	}
	for _, tt := range tests {
		if got := tt.err.Error(); got != tt.want {
			t.Errorf("%T.Error() = %q, want %q", tt.err, got, tt.want)
		}
	}
}

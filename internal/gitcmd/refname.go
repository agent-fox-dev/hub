package gitcmd

import (
	"fmt"
	"strings"
)

// ValidateRefName reports whether name is acceptable as a git branch or ref
// name passed positionally to git. It mirrors the rules of
// `git check-ref-format --branch` and additionally rejects names that start
// with '-' so that a value taken from an API request can never be parsed by
// git as a command-line option (for example `--exec=<cmd>` for rebase or
// `--detach` for checkout).
//
// Every GitRunner helper that takes a ref name also passes
// `--end-of-options` (or `--`) to git; this function is the first line of
// defence at the API boundary so that invalid names are reported to the
// client as 400 instead of surfacing as opaque git errors.
func ValidateRefName(name string) error {
	if name == "" {
		return fmt.Errorf("ref name must not be empty")
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("ref name must not start with '-'")
	}
	if name == "@" {
		return fmt.Errorf("ref name must not be '@'")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("ref name must not contain control characters")
		}
		switch r {
		case ' ', '~', '^', ':', '?', '*', '[', '\\':
			return fmt.Errorf("ref name must not contain %q", r)
		}
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("ref name must not contain '..'")
	}
	if strings.Contains(name, "@{") {
		return fmt.Errorf("ref name must not contain '@{'")
	}
	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.Contains(name, "//") {
		return fmt.Errorf("ref name must not start or end with '/' or contain '//'")
	}
	if strings.HasSuffix(name, ".") {
		return fmt.Errorf("ref name must not end with '.'")
	}
	for _, comp := range strings.Split(name, "/") {
		if strings.HasPrefix(comp, ".") {
			return fmt.Errorf("ref name must not have a path component starting with '.'")
		}
		if strings.HasSuffix(comp, ".lock") {
			return fmt.Errorf("ref name must not have a path component ending with '.lock'")
		}
	}
	return nil
}

// endOfOptions is the git option terminator: everything after it is treated
// as a positional argument even when it starts with '-'. Supported by every
// parse-options based subcommand since git 2.24 (the runner requires 2.38).
const endOfOptions = "--end-of-options"

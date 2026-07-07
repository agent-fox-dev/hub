// Package cli implements the afc command-line client for af-hub.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
)

// PrintJSON marshals v as indented JSON and writes it to w, followed by a newline.
// All machine-readable output goes through this function to ensure it lands on stdout.
func PrintJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// PrintError writes a human-readable error message to w, followed by a newline.
// All status/error messages go through this function to ensure they land on stderr.
func PrintError(w io.Writer, msg string) {
	fmt.Fprintln(w, "Error: "+msg)
}

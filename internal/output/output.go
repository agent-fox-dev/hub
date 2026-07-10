// Package output provides formatting utilities for CLI output. JSON API
// responses are pretty-printed with 2-space indentation to stdout; human-
// readable status and error messages are written to stderr.
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// PrintJSON writes the given raw JSON bytes to w, pretty-printed with 2-space
// indentation. The output is the raw server response with all original fields
// preserved — no reshaping, filtering, or transforming.
func PrintJSON(w io.Writer, data []byte) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, data, "", "  "); err != nil {
		return fmt.Errorf("failed to format JSON output: %w", err)
	}
	buf.WriteByte('\n')
	_, err := buf.WriteTo(w)
	return err
}

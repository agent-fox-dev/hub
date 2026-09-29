package gitserver

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

// Fetch and clone (git-upload-pack) are served by the git CLI in
// --stateless-rpc mode rather than by go-git's server transport.
//
// go-git's upload-pack server does not implement the stateless (smart HTTP)
// negotiation protocol: it ignores "have" lines and the "done" marker and
// always answers with a packfile. A client that already holds part of the
// history sends a negotiation-only round first (haves, no "done") and
// expects only ACK/NAK lines back; receiving "NAK" followed by raw pack data
// makes it fail with "fatal: protocol error: bad line length character:
// PACK". It also means every fetch transfers the full history.
//
// The git CLI is already a hard runtime dependency of the hub (see
// internal/gitcmd), so delegating to `git upload-pack` gives us the
// reference implementation of negotiation, multi_ack_detailed, side-band-64k,
// shallow, and filter support for free.

// uploadPackTimeout bounds a single upload-pack invocation whose request
// context carries no deadline of its own.
var uploadPackTimeout = 30 * time.Minute

// gitBinary resolves the git executable used for upload-pack.
var gitBinary = func() (string, error) { return exec.LookPath("git") }

// uploadPackRepoPath returns the on-disk path of the workspace repository
// for the current request, as resolved by gitResolverMiddleware.
func uploadPackRepoPath(c echo.Context) string {
	root, _ := c.Get("workspace_root").(string)
	slug := strings.TrimSuffix(c.Param("slug.git"), ".git")
	return filepath.Join(root, slug, "trunk")
}

// uploadPackCommand builds a `git upload-pack --stateless-rpc` command for
// repoPath. extraArgs are inserted before the repository path.
func uploadPackCommand(ctx context.Context, repoPath string, extraArgs ...string) (*exec.Cmd, error) {
	bin, err := gitBinary()
	if err != nil {
		return nil, fmt.Errorf("git executable not found: %w", err)
	}
	args := append([]string{"upload-pack", "--stateless-rpc"}, extraArgs...)
	args = append(args, repoPath)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		// Speak protocol v0 only; this matches the advertisement we emit.
		"GIT_PROTOCOL=",
	)
	cmd.WaitDelay = 5 * time.Second
	return cmd, nil
}

// writeUploadPackAdvertisement writes the ref advertisement for
// git-upload-pack (the body after the "# service=" announcement and flush)
// using `git upload-pack --stateless-rpc --advertise-refs`.
func writeUploadPackAdvertisement(c echo.Context) {
	ctx := c.Request().Context()
	cmd, err := uploadPackCommand(ctx, uploadPackRepoPath(c), "--advertise-refs")
	if err != nil {
		writeSessionError(c.Response(), err)
		return
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		log.Printf("git upload-pack --advertise-refs failed: %v: %s", err, strings.TrimSpace(stderr.String()))
		writeSessionError(c.Response(), fmt.Errorf("failed to advertise references"))
		return
	}
	_, _ = c.Response().Write(out)
}

// flushWriter flushes the HTTP response after every write so that pack data
// and side-band progress messages stream to the client as they are produced.
type flushWriter struct {
	w io.Writer
	f http.Flusher
}

func (fw flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return n, err
}

// serveUploadPack runs `git upload-pack --stateless-rpc` with the request
// body on stdin and streams its stdout to the response. The response headers
// (200, Content-Type) must already have been written by the caller.
//
// If git produces no output at all (empty or malformed request), a pkt-line
// ERR message is written instead so the client gets a protocol-level error.
func serveUploadPack(c echo.Context) {
	var body io.Reader = requestBody(c)

	// Git clients gzip large negotiation requests (many "have" lines).
	if strings.EqualFold(c.Request().Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(body)
		if err != nil {
			writeSessionError(c.Response(), fmt.Errorf("invalid gzip request body: %w", err))
			return
		}
		defer gz.Close()
		body = gz
	}

	// Reject an empty request up front: git would exit silently.
	br := bufio.NewReader(body)
	if _, err := br.Peek(1); err != nil {
		writeSessionError(c.Response(), fmt.Errorf("empty upload-pack request"))
		return
	}

	ctx := c.Request().Context()
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, uploadPackTimeout)
		defer cancel()
	}

	cmd, err := uploadPackCommand(ctx, uploadPackRepoPath(c))
	if err != nil {
		writeSessionError(c.Response(), err)
		return
	}
	var stderr bytes.Buffer
	cmd.Stdin = br
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		writeSessionError(c.Response(), err)
		return
	}
	if err := cmd.Start(); err != nil {
		writeSessionError(c.Response(), err)
		return
	}

	var flusher http.Flusher
	if f, ok := c.Response().Writer.(http.Flusher); ok {
		flusher = f
	}
	written, copyErr := io.Copy(flushWriter{w: c.Response(), f: flusher}, stdout)
	if copyErr != nil {
		// Drain so git is not blocked on a full pipe before we wait on it.
		_, _ = io.Copy(io.Discard, stdout)
	}
	waitErr := cmd.Wait()

	msg := strings.TrimSpace(stderr.String())
	switch {
	case written == 0 && (waitErr != nil || copyErr != nil):
		log.Printf("git upload-pack failed: %v: %s", waitErr, msg)
		if msg == "" {
			msg = "upload-pack failed"
		}
		writeSessionError(c.Response(), fmt.Errorf("%s", firstLine(msg)))
	case written == 0:
		writeSessionError(c.Response(), fmt.Errorf("invalid upload-pack request"))
	case copyErr != nil:
		log.Printf("git upload-pack: failed to stream response: %v", copyErr)
	case waitErr != nil:
		log.Printf("git upload-pack: exited with error: %v: %s", waitErr, msg)
	}
}

// firstLine returns the first line of s.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

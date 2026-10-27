// Command sandbox-lease turns a sandbox lease issued by the hub into a running
// sandbox, works in it and ends it. It is what a lease holder does after
//
//	afc sandbox create <workspace> --token-file lease.token > lease.json
//
// The lease is the right to one sandbox; the hub does not start it. The
// holder dials the hub's sandbox gateway at the lease's gateway_url with the
// lease token, creates the sandbox there and drives it with AgentKit's
// client, exactly as it would drive a sandbox on its own machine:
//
//	go run ./examples/sandbox-lease --lease lease.json --token-file lease.token
//	go run ./examples/sandbox-lease --lease lease.json --dir . --run 'go test ./...' --out results
//
// The hub builds the sandbox from the lease (image, network, resources,
// secrets), not from what the client asks for, so this program sends an
// empty spec. Closing the sandbox ends the lease as destroyed, with
// end_reason client.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
)

const usage = `usage: sandbox-lease --lease FILE [--token-file FILE] [--dir DIR] [--run CMD]... [--out DIR]

Creates the sandbox of a lease from "afc sandbox create", runs commands in it
and closes it, which ends the lease.

flags:
`

// options are the program's flags.
type options struct {
	lease     string   // the lease JSON afc printed; - is stdin
	tokenFile string   // the token file afc wrote with --token-file
	dir       string   // a host directory copied into the sandbox's workspace
	run       []string // shell commands to run in the sandbox, in order
	out       string   // a host directory the workspace is exported to
}

func main() {
	opts, err := parseFlags(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		os.Exit(2)
	}
	// An interrupt cancels the work but not the cleanup: the sandbox is
	// still closed, so the lease ends now rather than at its expires_at.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Stdout, os.Stdin, opts); err != nil {
		fmt.Fprintln(os.Stderr, "sandbox-lease:", err)
		os.Exit(1)
	}
}

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, "; ") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

func parseFlags(args []string, w io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("sandbox-lease", flag.ContinueOnError)
	fs.SetOutput(w)
	fs.Usage = func() { fmt.Fprint(w, usage); fs.PrintDefaults() }
	fs.StringVar(&o.lease, "lease", "", "the lease JSON printed by afc sandbox create; - reads it from stdin")
	fs.StringVar(&o.tokenFile, "token-file", "", "the file afc sandbox create --token-file wrote; needed when the lease JSON has no token")
	fs.StringVar(&o.dir, "dir", "", "copy this host directory into the sandbox's workspace first")
	fs.Var((*stringList)(&o.run), "run", "a shell command to run in the sandbox's workspace; repeatable (default: a short tour)")
	fs.StringVar(&o.out, "out", "", "export the sandbox's workspace into this host directory before closing")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.lease == "" || fs.NArg() > 0 {
		fs.Usage()
		return o, errors.New("usage")
	}
	return o, nil
}

// lease is the part of the hub's lease representation this program reads
// (see "The lease representation" in docs/api.md).
type lease struct {
	ID              string    `json:"id"`
	Workspace       string    `json:"workspace"`
	Profile         string    `json:"profile"`
	State           string    `json:"state"`
	GatewayURL      string    `json:"gateway_url"`
	ExpiresAt       time.Time `json:"expires_at"`
	LeaseExpiresAt  time.Time `json:"lease_expires_at"`
	Image           string    `json:"image"`
	Network         string    `json:"network"`
	Secrets         []string  `json:"secrets"`
	OptionalSecrets []string  `json:"optional_secrets"`
	// Token is present only in the answer that issued the lease, and only
	// when afc printed it rather than writing it to --token-file.
	Token string `json:"token"`
}

// readLease reads the lease JSON from path (- for stdin) and the token from
// tokenFile, or from the lease JSON when tokenFile is empty.
func readLease(path, tokenFile string, stdin io.Reader) (lease, error) {
	var l lease
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(stdin)
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return l, fmt.Errorf("reading the lease: %w", err)
	}
	if err := json.Unmarshal(raw, &l); err != nil {
		return l, fmt.Errorf("the lease is not the JSON afc sandbox create prints: %w", err)
	}
	if l.ID == "" || l.GatewayURL == "" {
		return l, errors.New("the lease has no id or no gateway_url: pass the JSON afc sandbox create prints")
	}
	if tokenFile != "" {
		tok, err := os.ReadFile(tokenFile)
		if err != nil {
			return l, fmt.Errorf("reading the lease token: %w", err)
		}
		l.Token = strings.TrimSpace(string(tok))
	}
	if l.Token == "" {
		return l, errors.New("no lease token: the lease JSON has none, so pass the file afc sandbox create --token-file wrote")
	}
	return l, nil
}

// run creates the lease's sandbox, works in it and closes it.
func run(ctx context.Context, w io.Writer, stdin io.Reader, o options) error {
	l, err := readLease(o.lease, o.tokenFile, stdin)
	if err != nil {
		return err
	}
	// Only a leased lease can create its sandbox, once. Saying so here is
	// clearer than the gateway's refusal.
	if l.State != "" && l.State != "leased" {
		return fmt.Errorf("lease %s is %s: only a leased lease can create its sandbox; issue a new one with afc sandbox create %s", l.ID, l.State, l.Workspace)
	}
	fmt.Fprintf(w, "lease %s: workspace %s, profile %s, image %s, network %s\n", l.ID, l.Workspace, l.Profile, l.Image, l.Network)
	if !l.LeaseExpiresAt.IsZero() {
		fmt.Fprintf(w, "  create the sandbox before %s; it lives until %s\n", l.LeaseExpiresAt.Local().Format(time.TimeOnly), l.ExpiresAt.Local().Format(time.TimeOnly))
	}

	// 1. Dial the hub's sandbox gateway. The token is checked on this first
	// call already: a lease that has ended or run out of time is refused as
	// unauthenticated, without saying which.
	p, err := gateway.Dial(ctx, l.GatewayURL, gateway.Options{Token: l.Token})
	if errors.Is(err, gateway.ErrUnauthenticated) {
		return fmt.Errorf("the gateway refused the lease token: the lease has ended or expired, or the token is not this lease's (afc sandbox get %s %s shows its state)", l.Workspace, l.ID)
	}
	if err != nil {
		return err
	}
	// Close destroys the sandboxes p created, then disconnects: a sandbox
	// left open by an error below still ends with the program.
	defer p.Close()
	gi := p.Gateway()
	fmt.Fprintf(w, "dialled %s: gateway %s, protocol %d.%d, backend %s\n", gi.URL, gi.Version, gi.ProtocolMajor, gi.ProtocolMinor, gi.Backend)

	// 2. Create the sandbox, copying --dir into its workspace, and get its
	// tools. The hub takes the image, network, resources, environment and
	// TTL from the lease and ignores the caller's; the spec repeats the
	// lease's only so that the client's own checks agree with the hub.
	spec, err := leaseSpec(l, p.Capabilities(), time.Now())
	if err != nil {
		return err
	}
	var src sandbox.Source
	if o.dir != "" {
		src = sandbox.LocalDir{Path: o.dir}
	}
	sb, tools, err := sandbox.Open(ctx, p, spec, src, sandbox.ToolOptions{})
	if err != nil {
		return fmt.Errorf("creating the sandbox: %w", err)
	}
	// The cleanup outlives an interrupt, so that the lease ends now.
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = sb.Close(cctx)
	}()
	fmt.Fprintf(w, "sandbox %s created: agent %s, workspace %s\n", sb.ID(), sb.Agent().Version, sb.Agent().Workdir)
	infos, err := p.List(ctx)
	if err != nil {
		return err
	}
	for _, in := range infos {
		fmt.Fprintf(w, "  labels %s\n", formatLabels(in.Labels))
	}

	// 3. Run the commands through the sandbox's execute tool, which runs them
	// in the sandbox's workspace, not on this machine. They are called
	// directly because they are the caller's own; an agent driven by a model
	// would get these same tools behind a guard.
	picked, err := core.Named(tools, "execute")
	if err != nil {
		return err
	}
	execute := picked[0]
	var failed error
	for _, s := range steps(o.run, l) {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("interrupted: %w", err)
		}
		in, _ := json.Marshal(map[string]string{"command": s.command})
		res := execute.Execute(ctx, in)
		fmt.Fprintf(w, "\n%s\n", s.title)
		out := strings.TrimRight(res.Text, "\n")
		if out == "" && res.Error != "" {
			out = res.Error + ": " + res.Detail
		}
		if out != "" {
			fmt.Fprintln(w, out)
		}
		if !res.OK && failed == nil {
			failed = fmt.Errorf("command %q failed: %s", s.command, res.Error)
		}
	}

	// An interrupt aborts the running command; the deferred Close ends the
	// lease.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("interrupted: %w", err)
	}

	// 4. Copy the workspace out, before the sandbox and its files are gone.
	if o.out != "" {
		if err := sb.Export(ctx, ".", sandbox.LocalDir{Path: o.out}); err != nil {
			return fmt.Errorf("exporting the workspace: %w", err)
		}
		fmt.Fprintf(w, "\nexported the workspace to %s\n", o.out)
	}

	// 5. Close the sandbox. The hub destroys it and ends the lease as
	// destroyed, end_reason client; the token stops working.
	if err := sb.Close(ctx); err != nil {
		// The lease stays active and keeps its sandbox; the hub's DELETE
		// destroys it, or the lease's expires_at does.
		return fmt.Errorf("closing the sandbox: %w\nthe lease is still active; end it with afc sandbox destroy %s %s", err, l.Workspace, l.ID)
	}
	fmt.Fprintf(w, "\nsandbox closed; lease %s has ended (afc sandbox get %s %s)\n", l.ID, l.Workspace, l.ID)
	return failed
}

// leaseSpec is the spec sent for the lease's sandbox. The hub ignores all of
// it but the labels and the working directory, but the client checks it
// before sending: an empty image becomes the client's default image (which a
// development build of AgentKit does not have), a zero TTL one hour (which
// may be more than the lease has left) and a zero network none (which may
// not be a mode the hub offers). So the spec says what the lease says.
func leaseSpec(l lease, caps sandbox.Capabilities, now time.Time) (sandbox.Spec, error) {
	ttl := l.ExpiresAt.Sub(now).Truncate(time.Second)
	if ttl <= 0 {
		return sandbox.Spec{}, fmt.Errorf("lease %s ran out of time at %s; issue a new one with afc sandbox create %s", l.ID, l.ExpiresAt.Local().Format(time.TimeOnly), l.Workspace)
	}
	spec := sandbox.Spec{TTL: ttl, Labels: map[string]string{"example": "sandbox-lease"}}
	if caps.Image {
		spec.Image = l.Image
	}
	for _, mode := range caps.Network {
		if mode.String() == l.Network {
			spec.Network.Mode = mode
		}
	}
	return spec, nil
}

// secretName is the hub's rule for a secret key, which also makes it safe to
// name in a shell command.
var secretName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,254}$`)

// step is one shell command to run in the sandbox, and the line shown
// before its output.
type step struct{ title, command string }

// steps are the --run commands, or without any a short tour: who and where
// the sandbox is, and which of the lease's secrets reached it.
func steps(run []string, l lease) []step {
	var s []step
	for _, cmd := range run {
		s = append(s, step{"$ " + cmd, cmd})
	}
	if len(run) > 0 {
		return s
	}
	for _, cmd := range []string{"id", "uname -srm", "ls -la"} {
		s = append(s, step{"$ " + cmd, cmd})
	}
	if check := secretCheck(append(slices.Clone(l.Secrets), l.OptionalSecrets...)); check != "" {
		s = append(s, step{"# which of the lease's secrets are set (values are not shown)", check})
	}
	return s
}

// secretCheck is a shell command that says which of the named secrets are
// set in the sandbox's environment, without printing a value; "" when there
// is no name to check.
func secretCheck(names []string) string {
	var lines []string
	for _, n := range names {
		if secretName.MatchString(n) {
			lines = append(lines, fmt.Sprintf(`if [ -n "${%s}" ]; then echo '%s: set'; else echo '%s: not set'; fi`, n, n, n))
		}
	}
	return strings.Join(lines, "\n")
}

// formatLabels renders labels sorted by key, as key=value.
func formatLabels(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for i, k := range keys {
		keys[i] = k + "=" + labels[k]
	}
	return strings.Join(keys, " ")
}

package sandboxes

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot returns the repository root, two levels above this package.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root %s has no go.mod: %v", root, err)
	}
	return root
}

// readRepoFile returns the content of a repository file as a string.
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(data)
}

// makeTarget returns the recipe of a Makefile target: the lines from its
// "<name>:" header up to the next blank line.
func makeTarget(t *testing.T, makefile, name string) string {
	t.Helper()
	idx := strings.Index(makefile, "\n"+name+":")
	if idx < 0 {
		t.Fatalf("Makefile has no %q target", name)
	}
	block := makefile[idx+1:]
	if end := strings.Index(block, "\n\n"); end >= 0 {
		block = block[:end]
	}
	return block
}

// TS-25-53: The build files wire the agentkit-go sibling the way they wire
// apikit: a require and a replace in go.mod, a build context in the
// Containerfile and the Makefile, and a clone in both workflows.
// Requirements: 25-REQ-10.1, 25-REQ-10.3
func TestBuildFiles_AgentkitSibling_TS_25_53(t *testing.T) {
	const (
		rootModule    = "github.com/agent-fox-dev/agentkit-go"
		sandboxModule = rootModule + "/sandbox"
	)

	t.Run("go.mod requires and replaces the sandbox module", func(t *testing.T) {
		gomod := readRepoFile(t, "go.mod")

		for _, mod := range []string{rootModule, sandboxModule} {
			require := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(mod) + `\s+v\S+`)
			if !require.MatchString(gomod) {
				t.Errorf("go.mod has no require for %s", mod)
			}
		}

		// The sandbox module is a separate Go module whose own replace of the
		// root module is ignored in dependents, so hub replaces both.
		for mod, dir := range map[string]string{
			rootModule:    "../agentkit-go",
			sandboxModule: "../agentkit-go/sandbox",
		} {
			replace := regexp.MustCompile(`(?m)^replace\s+` + regexp.QuoteMeta(mod) + `\s+=>\s+` + regexp.QuoteMeta(dir) + `\s*$`)
			if !replace.MatchString(gomod) {
				t.Errorf("go.mod has no `replace %s => %s`", mod, dir)
			}
		}
		if !strings.Contains(gomod, "replace github.com/txsvc/apikit => ../apikit") {
			t.Errorf("go.mod lost its apikit replace")
		}
	})

	t.Run("Containerfile copies the agentkit build context", func(t *testing.T) {
		containerfile := readRepoFile(t, "containers/hub/Containerfile")
		if !strings.Contains(containerfile, "COPY --from=agentkit . /build/agentkit-go") {
			t.Errorf("containers/hub/Containerfile does not copy the agentkit build context to /build/agentkit-go")
		}
	})

	t.Run("Makefile passes the agentkit build context", func(t *testing.T) {
		target := makeTarget(t, readRepoFile(t, "Makefile"), "build-hub-container")
		if !strings.Contains(target, "--build-context agentkit=../agentkit-go") {
			t.Errorf("build-hub-container target does not pass --build-context agentkit=../agentkit-go:\n%s", target)
		}
	})

	t.Run("workflows clone agentkit-go", func(t *testing.T) {
		const clone = "git clone https://github.com/agent-fox-dev/agentkit-go.git ../agentkit-go"
		for _, wf := range []string{
			".github/workflows/inc-build-hub.yaml",
			".github/workflows/build-hub-cli.yaml",
		} {
			if !strings.Contains(readRepoFile(t, wf), clone) {
				t.Errorf("%s does not clone agentkit-go next to the repository", wf)
			}
		}
		if !regexp.MustCompile(`(?m)^\s*agentkit=\.\./agentkit-go\s*$`).MatchString(readRepoFile(t, ".github/workflows/inc-build-hub.yaml")) {
			t.Errorf("inc-build-hub.yaml does not pass agentkit=../agentkit-go as a build context")
		}
	})
}

// TS-25-54: afc stays a static CGO_ENABLED=0 binary, so neither cmd/afc nor
// internal/cli may depend on internal/sandboxes or on any agentkit-go package.
// Requirement: 25-REQ-10.2
func TestAfcImportBoundary_TS_25_54(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not available; cannot list dependencies")
	}

	cmd := exec.Command(goBin, "list", "-deps", "./cmd/afc", "./internal/cli")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps failed: %v\n%s", err, out)
	}

	deps := strings.Fields(string(out))
	if len(deps) == 0 {
		t.Fatal("go list -deps returned no packages")
	}
	for _, dep := range deps {
		if strings.Contains(dep, "internal/sandboxes") || strings.Contains(dep, "agentkit-go") {
			t.Errorf("afc depends on %s", dep)
		}
	}
}

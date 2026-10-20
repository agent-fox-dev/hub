package forge

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	issuexPath = "github.com/agent-fox-dev/aflib/issuex"
	forgePath  = "github.com/agent-fox-dev/hub/internal/forge"
)

// repoRoot is the directory of the hub module.
func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	mod := strings.TrimSpace(string(out))
	if mod == "" || mod == os.DevNull {
		t.Fatal("not inside a Go module")
	}
	return filepath.Dir(mod)
}

// goInRepo runs the go tool in the module root with extra environment entries and
// returns its standard output. A failing run stops the test with the tool's output.
func goInRepo(t *testing.T, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// readRepoFile returns the content of a file of the repository.
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// TS-30-1: The aflib require, replace, Containerfile, Makefile and workflow wiring
// are present and the module builds.
// Requirement: 30-REQ-1.1
func TestAflibWiring_TS_30_1(t *testing.T) {
	gomod := readRepoFile(t, "go.mod")
	for _, want := range []string{
		"github.com/agent-fox-dev/aflib v0.0.0",
		"replace github.com/agent-fox-dev/aflib => ../aflib",
	} {
		if !strings.Contains(gomod, want) {
			t.Errorf("go.mod does not contain %q", want)
		}
	}

	if cf := readRepoFile(t, "containers/hub/Containerfile"); !strings.Contains(cf, "COPY --from=aflib . /build/aflib") {
		t.Error("the Containerfile does not copy the aflib build context to /build/aflib")
	}

	if mk := readRepoFile(t, "Makefile"); !strings.Contains(mk, "--build-context aflib=../aflib") {
		t.Error("the Makefile's build-hub-container does not pass --build-context aflib=../aflib")
	}

	const clone = "git clone https://github.com/agent-fox-dev/aflib.git ../aflib"
	for _, wf := range []string{".github/workflows/build-hub-cli.yaml", ".github/workflows/inc-build-hub.yaml"} {
		if !strings.Contains(readRepoFile(t, wf), clone) {
			t.Errorf("%s does not clone aflib next to the repository", wf)
		}
	}
	if !strings.Contains(readRepoFile(t, ".github/workflows/inc-build-hub.yaml"), "aflib=../aflib") {
		t.Error("inc-build-hub.yaml does not pass the aflib build context")
	}

	// The sibling checkout is there, as the replace expects, and the module builds.
	if _, err := os.Stat(filepath.Join(repoRoot(t), "..", "aflib", "issuex")); err != nil {
		t.Fatalf("../aflib/issuex is not a sibling checkout: %v", err)
	}
	goInRepo(t, nil, "build", "./...")
}

// TS-30-2: Only packages under internal/forge import issuex. The scan includes the
// imports of test files, so that no test of another package reaches issuex either.
// Requirement: 30-REQ-1.2
func TestIssuexImportedOnlyUnderInternalForge_TS_30_2(t *testing.T) {
	out := goInRepo(t, nil, "list", "-f",
		`{{.ImportPath}}|{{join .Imports " "}} {{join .TestImports " "}} {{join .XTestImports " "}}`, "./...")

	importers := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		pkg, imports, _ := strings.Cut(line, "|")
		for _, imp := range strings.Fields(imports) {
			if imp != issuexPath {
				continue
			}
			importers++
			if pkg != forgePath && !strings.HasPrefix(pkg, forgePath+"/") {
				t.Errorf("%s imports %s; only packages under internal/forge may", pkg, issuexPath)
			}
		}
	}
	// Without an importer the loop above would prove nothing.
	if importers == 0 {
		t.Errorf("no package of the hub imports %s", issuexPath)
	}
}

// TS-30-3: afc does not depend on internal/forge (nor on aflib) and builds with cgo
// disabled.
// Requirement: 30-REQ-1.3
func TestAfcFreeOfInternalForge_TS_30_3(t *testing.T) {
	forgeDeps := strings.Fields(goInRepo(t, nil, "list", "-deps", "./internal/forge"))
	found := false
	for _, dep := range forgeDeps {
		found = found || dep == issuexPath
	}
	if !found {
		t.Fatalf("internal/forge does not depend on %s, so the isolation below would prove nothing", issuexPath)
	}

	cgoOff := []string{"CGO_ENABLED=0"}
	afcDeps := strings.Fields(goInRepo(t, cgoOff, "list", "-deps", "./cmd/afc"))
	if len(afcDeps) == 0 {
		t.Fatal("go list -deps ./cmd/afc returned no packages")
	}
	for _, dep := range afcDeps {
		if strings.Contains(dep, "internal/forge") || strings.Contains(dep, "agent-fox-dev/aflib") {
			t.Errorf("afc depends on %s", dep)
		}
	}

	bin := filepath.Join(t.TempDir(), "afc")
	goInRepo(t, cgoOff, "build", "-o", bin, "./cmd/afc")
	if _, err := os.Stat(bin); err != nil {
		t.Errorf("the afc build left no binary: %v", err)
	}
}

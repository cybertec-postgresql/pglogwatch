package pglogwatch_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestImportsAreStandardLibraryOnly is the executable form of AC-021 and
// PKG-002: `go list -deps` on the root package must name only standard-library
// packages.
//
// It shells out to `go list` rather than walking imports with go/packages
// because go/packages lives in golang.org/x/tools, and adding a module to the
// dependency graph in order to prove the dependency graph is empty would be a
// self-defeating test. The go command is already required to run tests at all.
func TestImportsAreStandardLibraryOnly(t *testing.T) {
	deps := goListDeps(t, modulePath)
	for _, pkg := range deps {
		// go list -deps names the package itself last.
		if pkg == modulePath || strings.HasPrefix(pkg, modulePath+"/") {
			continue
		}
		if !isStdlib(pkg) {
			t.Errorf("root package depends on non-stdlib package %q; PKG-002 requires an empty dependency graph", pkg)
		}
	}
}

const modulePath = "github.com/cybertec-postgresql/pglogwatch"

// TestForbiddenImports covers CON-004, which singles out four standard-library
// packages the root package must not reach.
//
// Each is banned for a reason worth stating, since a future contributor will
// otherwise import one without noticing:
//
//   - net and database/sql would make the library capable of talking to a
//     server, which CON-005 forbids and which pgremote exists to provide
//     separately;
//   - os/exec would make it capable of running programs;
//   - encoding/json would make jsonlog parsing allocate per record, which is
//     the whole point of the hand-written scanner (PERF-005).
func TestForbiddenImports(t *testing.T) {
	forbidden := []string{"net", "os/exec", "database/sql", "encoding/json"}
	deps := goListDeps(t, modulePath)
	seen := make(map[string]bool, len(deps))
	for _, d := range deps {
		seen[d] = true
	}
	for _, f := range forbidden {
		if seen[f] {
			t.Errorf("root package imports %q, forbidden by CON-004", f)
		}
	}
}

func goListDeps(t *testing.T, pkg string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", pkg).Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	var deps []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			deps = append(deps, line)
		}
	}
	if len(deps) == 0 {
		t.Fatal("go list -deps returned nothing")
	}
	return deps
}

// isStdlib reports whether a package path belongs to the standard library.
//
// The test the go command itself uses: a standard-library import path has no
// dot in its first element, because every other path starts with a domain.
func isStdlib(pkg string) bool {
	first, _, _ := strings.Cut(pkg, "/")
	return !strings.Contains(first, ".")
}

package pglogwatch_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	// Both build configurations, because they compile different files.
	// PKG-007's purego fallback selects safe.go instead of unsafe.go, and
	// a dependency added to the file the default build does not compile
	// would be invisible to a single-configuration check while still
	// reaching every consumer who sets the tag.
	for _, tags := range []string{"", "purego"} {
		name := "default"
		if tags != "" {
			name = tags
		}
		t.Run(name, func(t *testing.T) {
			for _, pkg := range goListDeps(t, modulePath, tags) {
				// go list -deps names the package itself last.
				if pkg == modulePath || strings.HasPrefix(pkg, modulePath+"/") {
					continue
				}
				if !isStdlib(pkg) {
					t.Errorf("root package depends on non-stdlib package %q under tags %q; "+
						"PKG-002 requires an empty dependency graph", pkg, tags)
				}
			}
		})
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
	for _, tags := range []string{"", "purego"} {
		deps := goListDeps(t, modulePath, tags)
		seen := make(map[string]bool, len(deps))
		for _, d := range deps {
			seen[d] = true
		}
		for _, f := range forbidden {
			if seen[f] {
				t.Errorf("root package imports %q under tags %q, forbidden by CON-004", f, tags)
			}
		}
	}
}

func goListDeps(t *testing.T, pkg, tags string) []string {
	t.Helper()
	args := []string{"list", "-deps"}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	out, err := exec.Command("go", append(args, pkg)...).Output()
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

// TestNestedModulesDoNotReachTheRoot is PKG-004 stated as a test.
//
// The nested modules exist so that a consumer who only parses inherits
// nothing: pgx, klauspost/compress and ulikunitz/xz are real dependencies with
// real transitive graphs, and the moment one of them becomes reachable from
// the root package the module's central claim stops being true.
//
// The root go.mod is the thing to check rather than the import graph, because
// a require line is enough to affect a consumer's build even before any code
// imports it.
func TestNestedModulesDoNotReachTheRoot(t *testing.T) {
	data, err := os.ReadFile("go.mod")
	require.NoError(t, err)

	forbidden := []string{
		"github.com/jackc/pgx",
		"github.com/klauspost/compress",
		"github.com/ulikunitz/xz",
		"github.com/pashagolub/pgxmock",
	}
	for _, dep := range forbidden {
		assert.NotContains(t, string(data), dep,
			"PKG-004: %s belongs to a nested module and must not appear in the root go.mod", dep)
	}
}

// TestRootRequiresOnlyTestDependencies checks PKG-002 and PKG-003 together:
// the root module may require testify, and whatever testify itself needs, and
// nothing else.
//
// Listing the permitted set rather than counting it means a new dependency
// fails with its own name in the message.
func TestRootRequiresOnlyTestDependencies(t *testing.T) {
	data, err := os.ReadFile("go.mod")
	require.NoError(t, err)

	permitted := map[string]bool{
		"github.com/stretchr/testify":           true, // PKG-003
		"github.com/davecgh/go-spew":            true, // testify
		"github.com/pmezani/go-difflib":         true, // testify
		"github.com/pmezani/go-difflib/difflib": true,
		"go.yaml.in/yaml/v3":                    true, // testify
		"gopkg.in/yaml.v3":                      true, // testify
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "github.com/") &&
			!strings.HasPrefix(line, "gopkg.in/") &&
			!strings.HasPrefix(line, "go.yaml.in/") {
			continue
		}
		name, _, _ := strings.Cut(line, " ")
		assert.True(t, permitted[name],
			"unexpected dependency %q in the root go.mod; PKG-002 allows only test dependencies", name)
	}
}

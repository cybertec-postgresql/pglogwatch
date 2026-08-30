package pglogwatch_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"slices"
	"strings"
	"testing"
)

// maxExportedIdentifiers is CON-006's budget. The specification calls it a
// target; it is enforced as a limit here because an API budget that is not
// enforced is a preference, and VAL-007 makes it a release condition.
//
// What is counted: package-scope types, functions, variables and constants.
// Methods and struct fields are not, because counting them would put Record's
// thirty documented fields against a budget of forty and make the rule
// meaningless. That reading is what keeps the number reviewable: forty things
// a user has to learn the names of.
const maxExportedIdentifiers = 40

func TestExportedAPIBudget(t *testing.T) {
	names := exportedNames(t)
	if len(names) > maxExportedIdentifiers {
		slices.Sort(names)
		t.Errorf("root package exports %d identifiers, CON-006 allows %d:\n  %s",
			len(names), maxExportedIdentifiers, strings.Join(names, "\n  "))
	}
	t.Logf("%d of %d exported identifiers used", len(names), maxExportedIdentifiers)
}

// TestExportedAPIDocumented is VAL-007's second half: every exported
// identifier must carry a doc comment. A doc comment on the enclosing
// declaration counts, which is how a grouped const block documents its values.
func TestExportedAPIDocumented(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nonTestFile, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				checkDocumented(t, decl)
			}
		}
	}
}

func checkDocumented(t *testing.T, decl ast.Decl) {
	t.Helper()
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Recv != nil || !d.Name.IsExported() {
			return
		}
		if d.Doc == nil {
			t.Errorf("exported func %s has no doc comment", d.Name.Name)
		}
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				if s.Name.IsExported() && s.Doc == nil && d.Doc == nil {
					t.Errorf("exported type %s has no doc comment", s.Name.Name)
				}
			case *ast.ValueSpec:
				for _, n := range s.Names {
					if n.IsExported() && s.Doc == nil && d.Doc == nil {
						t.Errorf("exported %s %s has no doc comment", d.Tok, n.Name)
					}
				}
			}
		}
	}
}

func exportedNames(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nonTestFile, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				names = append(names, exportedFromDecl(decl)...)
			}
		}
	}
	return names
}

func exportedFromDecl(decl ast.Decl) []string {
	var names []string
	switch d := decl.(type) {
	case *ast.FuncDecl:
		// Methods are excluded; see maxExportedIdentifiers.
		if d.Recv == nil && d.Name.IsExported() {
			names = append(names, d.Name.Name)
		}
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				if s.Name.IsExported() {
					names = append(names, s.Name.Name)
				}
			case *ast.ValueSpec:
				for _, n := range s.Names {
					if n.IsExported() {
						names = append(names, n.Name)
					}
				}
			}
		}
	}
	return names
}

// nonTestFile selects the files that make up the package as a consumer sees
// it: test files may export whatever they need.
func nonTestFile(fi fs.FileInfo) bool {
	return !strings.HasSuffix(fi.Name(), "_test.go")
}

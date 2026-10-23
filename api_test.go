package pglogwatch_test

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"path/filepath"
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
// meaningless.
//
// An enumeration counts once: a named type and the typed constants declared
// with it (Severity, Format, Flags) are one design decision a reader learns
// together. The freeze review for v1.0.0 (T165) counted each value on its own
// and reached exactly 40, 22 of them enumeration values. CON-006 was amended
// when the message template helper (spec/message-template.md, CON-004) became
// the first post-v1.0 addition, so the budget measures the things a user has
// to learn the names of instead of the length of the severity list.
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
	for _, file := range packageFiles(t, parser.ParseComments) {
		for _, decl := range file.Decls {
			checkDocumented(t, decl)
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
	var names []string
	for _, file := range packageFiles(t, 0) {
		for _, decl := range file.Decls {
			names = append(names, exportedFromDecl(decl)...)
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
		// typ is the type a const spec has, explicit or repeated from the
		// spec above it, which is how "FormatCSV" in an iota block gets
		// the type Format without spelling it.
		var typ ast.Expr
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				if s.Name.IsExported() {
					names = append(names, s.Name.Name)
				}
			case *ast.ValueSpec:
				if s.Type != nil || len(s.Values) > 0 {
					typ = s.Type
				}
				if d.Tok == token.CONST && isExportedNamedType(typ) {
					// An enumeration value; its type is already counted.
					continue
				}
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

// isExportedNamedType reports whether typ names an exported type of this
// package, as opposed to a predeclared type such as int or none at all.
// Constants of such a type are the values of an enumeration.
func isExportedNamedType(typ ast.Expr) bool {
	id, ok := typ.(*ast.Ident)
	return ok && id.IsExported()
}

// packageFiles parses the files that make up the package as a consumer sees
// it: go/build's GoFiles excludes test files, which may export whatever they
// need, and applies build constraints for the current configuration, so a pair
// like safe.go and unsafe.go contributes only the file that is actually built.
// parser.ParseDir, which this replaced, is deprecated for ignoring those
// constraints.
func packageFiles(t *testing.T, mode parser.Mode) []*ast.File {
	t.Helper()
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(pkg.GoFiles))
	for _, name := range pkg.GoFiles {
		file, err := parser.ParseFile(fset, filepath.Join(pkg.Dir, name), nil, mode)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	return files
}

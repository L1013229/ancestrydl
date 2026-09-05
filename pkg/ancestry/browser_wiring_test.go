package ancestry

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

const (
	launcherPackage = "github.com/go-rod/rod/lib/launcher"
	launcherBuilder = "serverLauncher"
)

// launcherAlias reports the name a file uses for the launcher package, so an
// aliased import cannot slip past a hard-coded identifier. It is empty when
// the file does not import the package at all.
func launcherAlias(file *ast.File) string {
	for _, spec := range file.Imports {
		if strings.Trim(spec.Path.Value, `"`) != launcherPackage {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name
		}
		return "launcher"
	}
	return ""
}

// constructsLauncher reports whether a call expression is `alias.New(...)`.
func constructsLauncher(node ast.Node, alias string) bool {
	call, ok := node.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "New" {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && pkg.Name == alias
}

// launcherConstructions returns, per function declaration in the file, the
// positions at which that function builds a browser launcher of its own.
func launcherConstructions(file *ast.File, alias string) map[string][]token.Pos {
	found := map[string][]token.Pos{}
	for _, decl := range file.Decls {
		function, ok := decl.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			if constructsLauncher(node, alias) {
				found[function.Name.Name] = append(found[function.Name.Name], node.Pos())
			}
			return true
		})
	}
	return found
}

// TestServerLauncherIsHeadlessWithoutSandbox proves the helper carries the
// fork's two flags. It cannot prove the binary uses the helper, and that is
// the half an upstream sync breaks: upstream has no serverLauncher at all, so
// a merge that takes upstream's NewClient restores a bare launcher.New and
// leaves the flag test passing over a browser that cannot start on the server.
//
// So this reads the package's own syntax instead of its behaviour: every
// launcher.New in pkg/ancestry must sit inside serverLauncher. That binds the
// call sites rather than the helper, which is the direction the defect runs,
// and any new function that builds its own launcher fails here whatever it is
// called.
func TestEveryLauncherIsBuiltByServerLauncher(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("cannot enumerate the package: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no Go files found: this test would pass over an empty package")
	}

	fset := token.NewFileSet()
	total, inBuilder := 0, 0

	for _, name := range files {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("cannot parse %s: %v", name, err)
		}
		alias := launcherAlias(file)
		if alias == "" {
			continue
		}
		for function, positions := range launcherConstructions(file, alias) {
			total += len(positions)
			if function == launcherBuilder {
				inBuilder += len(positions)
				continue
			}
			for _, position := range positions {
				t.Errorf(
					"%s: %s builds its own browser launcher; every launcher must come from %s so the "+
						"headless and no-sandbox flags cannot be lost",
					fset.Position(position), function, launcherBuilder,
				)
			}
		}
	}

	// A package that stopped constructing launchers altogether would satisfy
	// the loop above without holding anything, so say what was actually seen.
	if total == 0 {
		t.Fatal("no launcher is constructed anywhere in the package; the flag test would pin dead code")
	}
	if inBuilder == 0 {
		t.Fatalf("%s builds no launcher; the flag test has nothing to hold", launcherBuilder)
	}
}

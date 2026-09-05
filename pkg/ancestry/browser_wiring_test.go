package ancestry

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestServerLauncherIsHeadlessWithoutSandbox proves the helper carries the
// fork's two flags. It cannot prove the binary uses the helper, and that is
// the half an upstream sync breaks: upstream has no serverLauncher at all, so
// a merge that takes upstream's NewClient restores a bare launcher.New() and
// leaves the flag test passing over a browser that cannot start on the server.
//
// So this reads the package's own syntax instead of its behaviour: every
// launcher.New in pkg/ancestry must sit inside serverLauncher. That binds the
// call sites rather than the helper, which is the direction the defect runs,
// and any new function that builds its own launcher fails here whatever it is
// called.
func TestEveryLauncherIsBuiltByServerLauncher(t *testing.T) {
	const builder = "serverLauncher"

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("cannot enumerate the package: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no Go files found: this test would pass over an empty package")
	}

	fset := token.NewFileSet()
	constructions := 0
	inBuilder := 0

	for _, name := range files {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("cannot parse %s: %v", name, err)
		}

		// The import name the file uses for the launcher package, so an alias
		// cannot slip past a hard-coded "launcher".
		local := ""
		for _, spec := range file.Imports {
			path := strings.Trim(spec.Path.Value, `"`)
			if path != "github.com/go-rod/rod/lib/launcher" {
				continue
			}
			local = "launcher"
			if spec.Name != nil {
				local = spec.Name.Name
			}
		}
		if local == "" {
			continue
		}

		ast.Inspect(file, func(node ast.Node) bool {
			decl, ok := node.(*ast.FuncDecl)
			if !ok {
				return true
			}
			ast.Inspect(decl.Body, func(inner ast.Node) bool {
				call, ok := inner.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "New" {
					return true
				}
				pkg, ok := selector.X.(*ast.Ident)
				if !ok || pkg.Name != local {
					return true
				}
				constructions++
				if decl.Name.Name == builder {
					inBuilder++
					return true
				}
				t.Errorf(
					"%s: %s builds its own browser launcher; every launcher must come from %s so the "+
						"headless and no-sandbox flags cannot be lost",
					fset.Position(call.Pos()), decl.Name.Name, builder,
				)
				return true
			})
			return true
		})
	}

	// A package that stopped constructing launchers altogether would satisfy
	// the loop above without holding anything, so say what was actually seen.
	if constructions == 0 {
		t.Fatalf("no %s.New call was found in the package; the flag test would then pin dead code", "launcher")
	}
	if inBuilder == 0 {
		t.Fatalf("%s does not build a launcher; the flag test has nothing to hold", builder)
	}
}

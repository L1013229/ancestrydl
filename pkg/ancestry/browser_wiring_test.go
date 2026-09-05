package ancestry

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	launcherPackage = "github.com/go-rod/rod/lib/launcher"
	rodPackage      = "github.com/go-rod/rod"
	launcherBuilder = "serverLauncher"
	controlURL      = "ControlURL"
)

// The DAQT fork runs Chromium on a container with no display, as a user for
// which the sandbox is unavailable. serverLauncher carries the two flags that
// make that work, and browser_test.go pins the flags themselves.
//
// The flags are the easy half. The half an upstream sync breaks is the wiring:
// upstream has no serverLauncher, so a merge that takes upstream's NewClient
// leaves serverLauncher present, its flag test green, and nothing calling it.
// The binary then launches a browser that cannot start on the server, and
// every test still passes.
//
// So these tests bind the path the executable takes rather than the helper:
//
//	main -> commands -> ancestry.NewClient -> serverLauncher -> launcher.New
//
// P1 only serverLauncher may touch the launcher package at all. Written as a
//    containment rule rather than a list of constructor names, so NewUserMode
//    or any constructor added later is refused without being enumerated.
// P2 every function that constructs a rod browser must hand it a control URL
//    that comes from serverLauncher. This is the edge an upstream sync drops:
//    a bare rod.New().MustConnect() launches rod's own browser with rod's own
//    flags, and P1 would not notice, because no launcher is constructed.
// P3 serverLauncher is called from somewhere other than itself, and a browser
//    is constructed somewhere. Without both, the rules above hold over dead
//    code and the flag test pins nothing.
//
// Scope is derived: the module root is found by walking up to go.mod and every
// non-test Go file under it is read. The previous version globbed *.go in this
// one directory, so a launcher built in commands/ or main.go was invisible to
// the control written to stop launchers being built elsewhere. Test files are
// excluded deliberately: they are not the path the executable takes.

// moduleRoot walks up from the package directory to the directory holding
// go.mod, so the scan covers the module rather than whichever directory the
// test happens to run in.
func moduleRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("cannot locate the package directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("no go.mod found above the package; the scan would have no scope")
		}
		directory = parent
	}
}

// moduleSources returns every non-test Go file in the module: the source the
// built binary is made of.
func moduleSources(t *testing.T) []string {
	t.Helper()
	root := moduleRoot(t)
	var found []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if name := entry.Name(); name == "vendor" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("cannot enumerate the module: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("no Go source found in the module; every rule below would hold vacuously")
	}
	return found
}

// importAlias reports the name a file uses for an imported package, so an
// aliased import cannot slip past a hard-coded identifier. It is empty when
// the file does not import the package at all.
func importAlias(file *ast.File, path string) string {
	for _, spec := range file.Imports {
		if strings.Trim(spec.Path.Value, `"`) != path {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name
		}
		return path[strings.LastIndex(path, "/")+1:]
	}
	return ""
}

// selectsOn reports whether a node is a call of the form `alias.Anything(...)`,
// and returns the member name. Any member counts: the rule is that only
// serverLauncher may reach the package, not that a particular constructor is
// banned.
func selectsOn(node ast.Node, alias string) (string, bool) {
	call, ok := node.(*ast.CallExpr)
	if !ok {
		return "", false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok || pkg.Name != alias {
		return "", false
	}
	return selector.Sel.Name, true
}

// callsFunction reports whether anything inside a node calls the named
// package-local function.
func callsFunction(node ast.Node, name string) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if identifier, ok := call.Fun.(*ast.Ident); ok && identifier.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

// tracesToBuilder reports whether an expression handed to ControlURL comes
// from serverLauncher, either directly or through a value assigned to it
// earlier in the same function.
func tracesToBuilder(function *ast.FuncDecl, argument ast.Expr) bool {
	if callsFunction(argument, launcherBuilder) {
		return true
	}
	identifier, ok := argument.(*ast.Ident)
	if !ok {
		return false
	}
	traced := false
	ast.Inspect(function.Body, func(node ast.Node) bool {
		var names []ast.Expr
		var values []ast.Expr
		switch statement := node.(type) {
		case *ast.AssignStmt:
			names, values = statement.Lhs, statement.Rhs
		case *ast.ValueSpec:
			for _, name := range statement.Names {
				names = append(names, name)
			}
			values = statement.Values
		default:
			return true
		}
		for index, name := range names {
			target, ok := name.(*ast.Ident)
			if !ok || target.Name != identifier.Name || index >= len(values) {
				continue
			}
			if callsFunction(values[index], launcherBuilder) {
				traced = true
				return false
			}
		}
		return true
	})
	return traced
}

type wiring struct {
	builderCalls  int
	browserBuilds int
	launcherUses  int
}

// launcherUsesIn applies P1 to one function and reports how many times the
// launcher package was reached from it.
func launcherUsesIn(t *testing.T, fset *token.FileSet, function *ast.FuncDecl, alias string) int {
	t.Helper()
	if alias == "" {
		return 0
	}
	uses := 0
	ast.Inspect(function.Body, func(node ast.Node) bool {
		member, ok := selectsOn(node, alias)
		if !ok {
			return true
		}
		uses++
		if function.Name.Name != launcherBuilder {
			t.Errorf(
				"%s: %s calls %s.%s; only %s may reach the launcher package, so the "+
					"headless and no-sandbox flags cannot be lost",
				fset.Position(node.Pos()), function.Name.Name, alias, member, launcherBuilder,
			)
		}
		return true
	})
	return uses
}

// browserWiringIn collects, for one function, where it constructs a rod
// browser and every expression it hands to ControlURL.
func browserWiringIn(function *ast.FuncDecl, alias string) ([]token.Pos, []ast.Expr) {
	if alias == "" {
		return nil, nil
	}
	var builds []token.Pos
	var urls []ast.Expr
	ast.Inspect(function.Body, func(node ast.Node) bool {
		if member, ok := selectsOn(node, alias); ok && member == "New" {
			builds = append(builds, node.Pos())
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selector, ok := call.Fun.(*ast.SelectorExpr); ok &&
			selector.Sel.Name == controlURL && len(call.Args) == 1 {
			urls = append(urls, call.Args[0])
		}
		return true
	})
	return builds, urls
}

// analyseFunction applies P1 and P2 to one function declaration.
func analyseFunction(
	t *testing.T, fset *token.FileSet, function *ast.FuncDecl, launcherName, rodName string,
) wiring {
	t.Helper()
	seen := wiring{launcherUses: launcherUsesIn(t, fset, function, launcherName)}

	if function.Name.Name != launcherBuilder && callsFunction(function.Body, launcherBuilder) {
		seen.builderCalls++
	}

	builds, urls := browserWiringIn(function, rodName)
	seen.browserBuilds = len(builds)
	if len(builds) == 0 {
		return seen
	}
	for _, url := range urls {
		if tracesToBuilder(function, url) {
			return seen
		}
	}
	// P2
	t.Errorf(
		"%s: %s constructs a browser without a %s that comes from %s; rod would launch its "+
			"own browser, without the flags the server needs",
		fset.Position(builds[0]), function.Name.Name, controlURL, launcherBuilder,
	)
	return seen
}

// scanWiring applies P1 and P2 across the module and reports what it saw, so
// P3 can refuse a module in which the rules held over nothing.
func scanWiring(t *testing.T) wiring {
	t.Helper()
	fset := token.NewFileSet()
	seen := wiring{}

	for _, path := range moduleSources(t) {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("cannot parse %s: %v", path, err)
		}
		launcherName := importAlias(file, launcherPackage)
		rodName := importAlias(file, rodPackage)
		if launcherName == "" && rodName == "" {
			continue
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			found := analyseFunction(t, fset, function, launcherName, rodName)
			seen.builderCalls += found.builderCalls
			seen.browserBuilds += found.browserBuilds
			seen.launcherUses += found.launcherUses
		}
	}
	return seen
}

// TestTheExecutablePathReachesServerLauncher holds P1, P2 and P3 over the
// module's non-test source: the code the built binary is made of.
func TestTheExecutablePathReachesServerLauncher(t *testing.T) {
	seen := scanWiring(t)

	// P3. Each of these would let the rules above pass over dead code.
	if seen.launcherUses == 0 {
		t.Errorf("nothing in the module builds a launcher; the flag test would pin dead code")
	}
	if seen.browserBuilds == 0 {
		t.Errorf("nothing in the module constructs a browser; there is no executable path to hold")
	}
	if seen.builderCalls == 0 {
		t.Errorf(
			"nothing outside %s calls it; the flags are pinned on a function the binary never reaches",
			launcherBuilder,
		)
	}
}

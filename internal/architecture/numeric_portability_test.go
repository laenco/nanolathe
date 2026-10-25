package architecture

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Numeric portability is stricter than the floating-storage allowance: every
// conversion outside the kernel must name its narrowing store (I2/I3), and
// library arithmetic must have a platform-independent rounding contract.
// It reads numericGuardDirs: the authoritative packages and the load-time
// packages that compute what the simulation reads.
func TestAuthoritativeNumericPortability(t *testing.T) {
	root := repositoryRoot(t)
	failures := numericPortabilityViolations(root, loadTypedPackages(t, root, numericGuardDirs()))
	if len(failures) != 0 {
		t.Fatal(strings.Join(failures, "\n"))
	}
}

func numericPortabilityViolations(root string, packages []typedPackage) []string {
	var failures []string
	for _, pkg := range packages {
		for _, file := range pkg.files {
			path, _ := filepath.Rel(root, pkg.fset.Position(file.Pos()).Filename)
			path = filepath.ToSlash(path)
			kernel := strings.HasPrefix(path, "internal/sim/numeric/")
			ast.Inspect(file, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok && !kernel && len(call.Args) == 1 && pkg.info.Types[call.Fun].IsType() {
					source := numericKinds(pkg.info.TypeOf(call.Args[0]))
					target := numericKinds(pkg.info.TypeOf(call))
					if source&types.IsFloat != 0 && target&types.IsInteger != 0 {
						failures = append(failures, fmt.Sprintf("%s:%d: floating-to-integer conversion must use numeric narrowing helper", path, pkg.fset.Position(call.Pos()).Line))
					}
				}
				// Check the referenced function itself, including a value assigned to a
				// local or called through an alias. Package identity survives import names.
				id, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				fn, ok := pkg.info.Uses[id].(*types.Func)
				if !ok || fn.Pkg() == nil || fn.Pkg().Path() != "math" {
					return true
				}
				if portableMathFunction(fn.Name()) {
					return true
				}
				failures = append(failures, fmt.Sprintf("%s:%d: math.%s must use the portable numeric kernel", path, pkg.fset.Position(id.Pos()).Line, fn.Name()))
				return true
			})
		}
	}
	sort.Strings(failures)
	return failures
}

// A generic conversion is also a narrowing site. Walk its constraint terms so
// named constraints and unions cannot hide a floating operand or integer target.
func numericKinds(typ types.Type) types.BasicInfo {
	switch typ := typ.Underlying().(type) {
	case *types.Basic:
		return typ.Info()
	case *types.Interface:
		var kinds types.BasicInfo
		for i := 0; i < typ.NumEmbeddeds(); i++ {
			kinds |= numericKinds(typ.EmbeddedType(i))
		}
		return kinds
	case *types.Union:
		var kinds types.BasicInfo
		for i := 0; i < typ.Len(); i++ {
			kinds |= numericKinds(typ.Term(i).Type())
		}
		return kinds
	}
	return 0
}

func portableMathFunction(name string) bool {
	switch name {
	// The exhaustive exactly-rounded arithmetic list in I2.
	case "Sqrt", "Abs", "Floor", "Ceil", "Trunc", "Round", "RoundToEven":
		return true
	// These inspect or construct IEEE-754 encodings; they do not approximate a
	// floating arithmetic result. NaN constructs one fixed encoding, not a
	// platform-dependent propagated payload (Distance retains that Unknown).
	case "IsNaN", "IsInf", "Signbit", "Float32bits", "Float64bits", "Float32frombits", "Float64frombits", "Inf", "NaN", "Copysign":
		return true
	}
	return false
}

func numericFixture(t *testing.T, root, source string) typedPackage {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(root, "fixture.go"), source, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Types: map[ast.Expr]types.TypeAndValue{}}
	_, err = (&types.Config{Importer: importer.ForCompiler(fset, "gc", goListExport)}).Check("fixture", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatal(err)
	}
	return typedPackage{importPath: "fixture", fset: fset, files: []*ast.File{file}, info: info}
}

// goListExport opens an imported package's export data where the go command
// reports it, as loadTypedPackages does. importer.Default finds the standard
// library through the test binary's GOROOT, which -trimpath (tools/go-budget)
// leaves empty unless the go command exported one while switching
// toolchains, so a host whose installed Go is the module's own failed here.
func goListExport(path string) (io.ReadCloser, error) {
	out, err := exec.Command("go", "list", "-export", "-f", "{{.Export}}", path).Output()
	if err != nil {
		return nil, fmt.Errorf("go list -export %s: %w", path, err)
	}
	export := strings.TrimSpace(string(out))
	if export == "" {
		return nil, fmt.Errorf("no export data for %s", path)
	}
	return os.Open(export)
}

func TestNumericPortabilityDetectsNamedAndAliasConversions(t *testing.T) {
	root := t.TempDir()
	pkg := numericFixture(t, root, `package fixture
 type real float32
 type signed int32
 type alias = signed
 type floating = real
 func convert(v floating) { _ = signed(v); _ = alias(v); _ = uint64(v); _ = int64(float64(v)); _ = real(4); _ = signed(4) }
 `)
	got := numericPortabilityViolations(root, []typedPackage{pkg})
	if len(got) != 4 {
		t.Fatalf("conversion violations = %v, want four", got)
	}
}

func TestNumericPortabilityFindsAliasedLibraryFunctions(t *testing.T) {
	for _, source := range []string{
		`package fixture; import m "math"; var indirect = m.Sin; func f(v float64) float64 { return m.Hypot(v,v)+indirect(v) }`,
		`package fixture; import . "math"; var indirect = Sin; func f(v float64) float64 { return Hypot(v,v)+indirect(v) }`,
	} {
		root := t.TempDir()
		got := numericPortabilityViolations(root, []typedPackage{numericFixture(t, root, source)})
		if len(got) != 2 {
			t.Fatalf("library violations = %v, want two", got)
		}
	}
}

func TestNumericPortabilityAllowsExactArithmeticAndEncoding(t *testing.T) {
	root := t.TempDir()
	pkg := numericFixture(t, root, `package fixture
 import "math"
 var _ = math.Sqrt; var _ = math.Abs; var _ = math.Floor; var _ = math.Ceil; var _ = math.Trunc; var _ = math.Round; var _ = math.RoundToEven
 var _ = math.IsNaN; var _ = math.IsInf; var _ = math.Signbit; var _ = math.Float32bits; var _ = math.Float64bits; var _ = math.Float32frombits; var _ = math.Float64frombits; var _ = math.Inf; var _ = math.NaN; var _ = math.Copysign
 `)
	if got := numericPortabilityViolations(root, []typedPackage{pkg}); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestNumericPortabilityDetectsGenericConversions(t *testing.T) {
	root := t.TempDir()
	pkg := numericFixture(t, root, `package fixture
 type floating interface { ~float32 | ~float64 }
 type signed interface { ~int32 | ~int64 }
 func convert[F floating, I signed](v F) I { return I(v) }
 func same[F floating](v F) F { return F(v) }
 func widen[I signed](v I) int64 { return int64(v) }
 `)
	got := numericPortabilityViolations(root, []typedPackage{pkg})
	if len(got) != 1 {
		t.Fatalf("generic conversion violations = %v, want one", got)
	}
}

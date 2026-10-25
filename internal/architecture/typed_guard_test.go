package architecture

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// typedPackage is one authoritative package checked with the same exported
// dependency information the Go tool uses. It lets the I1 guard identify a
// map through a local, selector, named type, or call expression.
type typedPackage struct {
	importPath string
	fset       *token.FileSet
	files      []*ast.File
	info       *types.Info
}

type listedPackage struct {
	ImportPath string
	Dir        string
	Export     string
	GoFiles    []string
}

func loadAuthoritativeTypedPackages(t *testing.T, root string) []typedPackage {
	t.Helper()
	return loadTypedPackages(t, root, authoritativeDirs)
}

// loadTypedPackages type-checks every package at or below dirs. The numeric
// guards call it with a wider list than the other typed audits.
func loadTypedPackages(t *testing.T, root string, dirs []string) []typedPackage {
	t.Helper()
	args := []string{"list", "-deps", "-export", "-json"}
	for _, dir := range dirs {
		args = append(args, "./"+dir+"/...")
	}
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("list authoritative package exports: %v", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(output))
	var listed []listedPackage
	exports := map[string]string{}
	for {
		var item listedPackage
		err := decoder.Decode(&item)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode authoritative package exports: %v", err)
		}
		listed = append(listed, item)
		exports[item.ImportPath] = item.Export
	}

	fset := token.NewFileSet()
	lookup := func(path string) (io.ReadCloser, error) {
		export, ok := exports[path]
		if !ok || export == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(export)
	}
	imp := importer.ForCompiler(fset, "gc", lookup)
	var packages []typedPackage
	for _, item := range listed {
		if !isImportPathUnder(item.ImportPath, dirs) {
			continue
		}
		files := make([]*ast.File, 0, len(item.GoFiles))
		for _, name := range item.GoFiles {
			file, err := parser.ParseFile(fset, filepath.Join(item.Dir, name), nil, 0)
			if err != nil {
				t.Fatalf("parse %s/%s: %v", item.ImportPath, name, err)
			}
			files = append(files, file)
		}
		info := &types.Info{
			Defs:  map[*ast.Ident]types.Object{},
			Uses:  map[*ast.Ident]types.Object{},
			Types: map[ast.Expr]types.TypeAndValue{},
		}
		if _, err := (&types.Config{Importer: imp}).Check(item.ImportPath, fset, files, info); err != nil {
			t.Fatalf("type-check %s: %v", item.ImportPath, err)
		}
		packages = append(packages, typedPackage{importPath: item.ImportPath, fset: fset, files: files, info: info})
	}
	return packages
}

func isImportPathUnder(path string, dirs []string) bool {
	const prefix = "github.com/nanolathe-gg/nanolathe/"
	for _, dir := range dirs {
		base := prefix + dir
		if path == base || strings.HasPrefix(path, base+"/") {
			return true
		}
	}
	return false
}

type typedMapRange struct {
	path         string
	function     string
	rangeOrdinal int
	rangeHash    string
	functionHash string
	body         string
	line         int
}

func typedMapRanges(root string, packages []typedPackage) []typedMapRange {
	var ranges []typedMapRange
	for _, pkg := range packages {
		for _, file := range pkg.files {
			path := pkg.fset.Position(file.Pos()).Filename
			relative, err := filepath.Rel(root, path)
			if err != nil {
				relative = path
			}
			appendRanges := func(name string, functionNode ast.Node, body *ast.BlockStmt) {
				rangeOrdinal := 0
				functionBody := normalizedNode(pkg.fset, functionNode)
				functionDigest := sha256.Sum256([]byte(functionBody))
				ast.Inspect(body, func(node ast.Node) bool {
					rangeStmt, ok := node.(*ast.RangeStmt)
					if !ok || !isMapType(pkg.info.TypeOf(rangeStmt.X)) {
						return true
					}
					rangeOrdinal++
					body := normalizedNode(pkg.fset, rangeStmt)
					rangeDigest := sha256.Sum256([]byte(body))
					ranges = append(ranges, typedMapRange{
						path:         filepath.ToSlash(relative),
						function:     name,
						rangeOrdinal: rangeOrdinal,
						rangeHash:    fmt.Sprintf("%x", rangeDigest[:]),
						functionHash: fmt.Sprintf("%x", functionDigest[:]),
						body:         body,
						line:         pkg.fset.Position(rangeStmt.Pos()).Line,
					})
					return true
				})
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				appendRanges(functionName(pkg.fset, fn), fn, fn.Body)
			}
			for _, decl := range file.Decls {
				if _, isFunction := decl.(*ast.FuncDecl); isFunction {
					continue
				}
				ast.Inspect(decl, func(node ast.Node) bool {
					literal, ok := node.(*ast.FuncLit)
					if !ok {
						return true
					}
					name := "package-init closure@" + strconv.Itoa(pkg.fset.Position(literal.Pos()).Line)
					appendRanges(name, literal, literal.Body)
					return false
				})
			}
		}
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].key() < ranges[j].key() })
	return ranges
}

func isMapType(typ types.Type) bool {
	if typ == nil {
		return false
	}
	_, ok := typ.Underlying().(*types.Map)
	return ok
}

func functionName(fset *token.FileSet, fn *ast.FuncDecl) string {
	if fn.Recv == nil {
		return fn.Name.Name
	}
	return normalizedNode(fset, fn.Recv.List[0].Type) + "." + fn.Name.Name
}

func normalizedNode(fset *token.FileSet, node ast.Node) string {
	var buffer bytes.Buffer
	if err := format.Node(&buffer, fset, node); err != nil {
		panic(err)
	}
	return strings.TrimSpace(buffer.String())
}

func (site typedMapRange) key() string {
	return site.path + " " + site.function + " " + site.rangeHash
}

func (site typedMapRange) functionKey() string {
	return site.path + " " + site.function
}

type floatScope struct {
	path  string
	name  string
	count int
	sites []occurrence
}

func float64Scopes(t *testing.T) []floatScope {
	t.Helper()
	var scopes []floatScope
	scanAuthoritativeSources(t, func(path string, fset *token.FileSet, file *ast.File, lineText func(int) string) {
		for _, decl := range file.Decls {
			if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.TYPE {
				for _, spec := range gen.Specs {
					typeSpec, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					if _, isStruct := typeSpec.Type.(*ast.StructType); isStruct {
						// Struct fields use resolved object identities below: a token
						// count cannot distinguish `a, b float64` from one field.
						continue
					}
					if scope, ok := float64ScopeForNode(path, fset, lineText, "type "+typeSpec.Name.Name, typeSpec); ok {
						scopes = append(scopes, scope)
					}
				}
				continue
			}
			name := floatScopeName(fset, decl)
			if name == "" {
				continue
			}
			if scope, ok := float64ScopeForNode(path, fset, lineText, name, decl); ok {
				scopes = append(scopes, scope)
			}
		}
	})
	sort.Slice(scopes, func(i, j int) bool { return scopes[i].key() < scopes[j].key() })
	return scopes
}

func float64ScopeForNode(path string, fset *token.FileSet, lineText func(int) string, name string, node ast.Node) (floatScope, bool) {
	perLine := map[int]string{}
	count := 0
	ast.Inspect(node, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.Ident:
			if node.Name == "float64" {
				count++
				line := fset.Position(node.Pos()).Line
				perLine[line] = lineText(line)
			}
		case *ast.BasicLit:
			if node.Kind == token.FLOAT {
				count++
				line := fset.Position(node.Pos()).Line
				perLine[line] = lineText(line)
			}
		}
		return true
	})
	if count == 0 {
		return floatScope{}, false
	}
	return floatScope{path: path, name: name, count: count, sites: siteList(perLine)}, true
}

func floatScopeName(fset *token.FileSet, decl ast.Decl) string {
	switch decl := decl.(type) {
	case *ast.FuncDecl:
		return "func " + functionName(fset, decl)
	case *ast.GenDecl:
		var names []string
		for _, spec := range decl.Specs {
			switch spec := spec.(type) {
			case *ast.TypeSpec:
				names = append(names, spec.Name.Name)
			case *ast.ValueSpec:
				for _, name := range spec.Names {
					names = append(names, name.Name)
				}
			}
		}
		if len(names) != 0 {
			return decl.Tok.String() + " " + strings.Join(names, ",")
		}
	}
	return ""
}

func (scope floatScope) key() string {
	return scope.path + " " + scope.name
}

type typedFloat64Field struct {
	path string
	key  string
	line int
}

func typedFloat64Fields(root string, packages []typedPackage) []typedFloat64Field {
	var fields []typedFloat64Field
	for _, pkg := range packages {
		for _, file := range pkg.files {
			filename := pkg.fset.Position(file.Pos()).Filename
			relative, err := filepath.Rel(root, filename)
			if err != nil {
				relative = filename
			}
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.TYPE {
					continue
				}
				for _, spec := range gen.Specs {
					typeSpec, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					structType, ok := typeSpec.Type.(*ast.StructType)
					if !ok {
						continue
					}
					path := filepath.ToSlash(relative)
					collectFloat64Fields(pkg, path, typeSpec.Name.Name, structType, &fields)
				}
			}
		}
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].key < fields[j].key })
	return fields
}

func collectFloat64Fields(pkg typedPackage, path, owner string, structType *ast.StructType, fields *[]typedFloat64Field) {
	for _, field := range structType.Fields.List {
		if nested, ok := field.Type.(*ast.StructType); ok {
			for _, name := range field.Names {
				collectFloat64Fields(pkg, path, owner+"."+name.Name, nested, fields)
			}
			continue
		}
		for _, name := range fieldNames(pkg.info, pkg.fset, field) {
			if !containsFloat64(name.typ) {
				continue
			}
			*fields = append(*fields, typedFloat64Field{
				path: path,
				key:  path + " type " + owner + "." + name.name + " " + normalizedNode(pkg.fset, field.Type),
				line: name.line,
			})
		}
	}
}

type namedField struct {
	name string
	typ  types.Type
	line int
}

func fieldNames(info *types.Info, fset *token.FileSet, field *ast.Field) []namedField {
	if len(field.Names) == 0 {
		return []namedField{{
			name: normalizedNode(fset, field.Type),
			typ:  info.TypeOf(field.Type),
			line: fset.Position(field.Pos()).Line,
		}}
	}
	fields := make([]namedField, 0, len(field.Names))
	for _, ident := range field.Names {
		object, _ := info.Defs[ident].(*types.Var)
		typ := info.TypeOf(field.Type)
		if object != nil {
			typ = object.Type()
		}
		fields = append(fields, namedField{
			name: ident.Name,
			typ:  typ,
			line: fset.Position(ident.Pos()).Line,
		})
	}
	return fields
}

// containsFloat64 inspects numeric storage within a field without promoting
// another named struct's fields into this owner's allowance. Named structs
// have their own declaration audit; signatures do not store numeric state.
func containsFloat64(typ types.Type) bool {
	seen := make(map[types.Type]bool)
	var visit func(types.Type) bool
	visit = func(typ types.Type) bool {
		if typ == nil || seen[typ] {
			return false
		}
		seen[typ] = true
		typ = types.Unalias(typ)
		if named, ok := typ.(*types.Named); ok {
			if _, ownsFields := named.Underlying().(*types.Struct); ownsFields {
				return false
			}
		}
		switch typ := typ.Underlying().(type) {
		case *types.Basic:
			return typ.Kind() == types.Float64
		case *types.Array:
			return visit(typ.Elem())
		case *types.Slice:
			return visit(typ.Elem())
		case *types.Pointer:
			return visit(typ.Elem())
		case *types.Map:
			return visit(typ.Key()) || visit(typ.Elem())
		case *types.Chan:
			return visit(typ.Elem())
		case *types.Struct:
			for i := 0; i < typ.NumFields(); i++ {
				if visit(typ.Field(i).Type()) {
					return true
				}
			}
		}
		return false
	}
	return visit(typ)
}

// mapRangeExceptions pins every currently justified map traversal by its
// package path, containing function, and normalized range body. The companion
// mapFunctionHashes binds the whole containing operation, including its sort
// or other ordering dependency, to the same I1 review.
var mapRangeExceptions = map[string]string{
	"internal/session/checkpoint_scenario.go checkpointScenarioRemovedHandles 4cc033a003a9b669ce09ba5e94e9ec72351a7eaff90c1afcb1cf7dd51b7ae237":         "gathers removed handles only, then sorts numerically before reading retained health values",
	"internal/session/checkpoint_visibility.go *Session.writeCheckpointVisibilityTail adb6a4fde822e2be9738128e112bde69bb74069256fee2ca92f32d75c783a5b6": "gathers stamp handles only, then sorts numerically before reading diagnostic visibility stamps",
	"internal/ai/checkpoint_profile.go checkpointSortedKeys 0c0133760bc905d4ee9e25f2065e15ac040e50cdd8188571a003cbf6b93c1088":                           "gathers map keys and sorts their exact string or numeric identities before checkpoint values",
	"internal/ai/checkpoint_profile.go *Profile.checkpointRecordIDs 27155c09f696a84913133702c4b4621afe6c25451693c75a455e281f744252a7":                   "gathers definition pointers then sorts admitted family, ordinal and key before encoding; pointer values never order rows",
	"internal/movement/checkpoint_layers.go checkpointLayerKeys 8c5bb2615b885b438f57b1ef835360be7683f5e383dfcece867598c77c93aed3":                       "copies class registry keys and sorts exact strings before layer discovery and writing",
	"internal/movement/checkpoint_provider.go checkpointRequestKeys 9aa03d2006fb5d6b6db275b4d90f7ad32b0eb99737901d20ce10a4096eadfefd":                   "copies actual request handles and sorts numerically before goal discovery, staged-index validation and writing",
	"internal/combat/checkpoint.go *Service.checkpointDeathHandles b9c1bd323a0df69c32079fef4e409031fd77665ace29df762e67017af5029ea2":                    "copies death-notification handles and sorts numerically before allocation discovery and writing",
	"internal/construction/checkpoint.go checkpointHandleKeys 4056f2b892f6d17dc1233d96e74376bfdf536fe4359f7b6597e3ce75ace48b5e":                         "copies raw product handle keys and numerically sorts them before any value read; no callbacks",
	"internal/path/checkpoint_write.go checkpointMapEntries 174c2f3b3b3b1875876863ab89bbcbc50d3d75775c6634c27ebb9a2e5349fd0c":                           "gathers cell keys and sorts (Z,X) before diagnostic value reads; absent-equivalent zero entries omitted, no callbacks",
	"internal/visibility/checkpoint.go *Service.checkpointObserverIDs 3a09d6228bec428a9acebdb242ea4b293c4fd2ad12e93545b4e683da85e4ddcb":                 "gathers observer keys and sorts numerically before diagnostic reads; no live cache or callback is touched",

	"internal/cob/debug_capture.go *VM.DebugSnapshot 7d182462d9e252dafb59da673acdcd673414aec86225c680a2c9710ae0bc1fa0":             "host-only read copies script names into detached storage and sorts them before returning; it neither invokes scripts nor changes VM state",
	"internal/features/debug_capture.go *Service.DebugSnapshot 0de7ab9b2ba8a95d0ba037338b670b09c17a342ce8e777aad0040ba750b1ddb8":   "host-only read gathers private keys and sorts them before copying feature values; it does not refresh the live key cache or change feature state",
	"internal/cob/binding.go bindStrict 86205b5f7e2c831877392e584559b68dedd881a5fa3fae0d7563637ac5276a20":                          "installs distinct handlers by port key; it does not invoke them",
	"internal/cob/checkpoint_ports.go checkpointPortKeys 42e9ea304ea2909bccff5ddfaa7e5c456034bb69a7c0b674064ae9c0e9b614e3":         "gathers port keys only, sorts numerically, then callers validate and encode copied rows without invoking callbacks",
	"internal/cob/binding.go bindStrict 4a375074326b6563496b3c5ce6f1f0f10af85196815ffe7d2b50a5eb84ba217c":                          "installs distinct bindings by port key; it does not invoke them",
	"internal/units/cob_binding.go bindUnitPortHandlers a4d09a5b9e3de58069318e1f534101f632a5819ec42cc2a8410811523b9fdb89":          "installs bindings only; no handler is run during this map walk",
	"internal/units/cob_binding.go bindCOBWithPortsAndVisibility cc431332ae7e03b1aa63d9ceaee1c96346a1c2ec2028a0f6e32296fd7d4a8f14": "installs bindings only; no handler is run during this map walk",
	"internal/features/service.go *Service.sortedInstanceKeys 0de7ab9b2ba8a95d0ba037338b670b09c17a342ce8e777aad0040ba750b1ddb8":    "gathers keys and sorts them before every consumer sees the slice",
	"internal/save/battle_image.go validateCarrierReferenceGraph 452bad5001b060b81ff4f96a4cf3a6dc90e812f0a597899aedfa32b66dd65c1a": "each traversal changes only local DFS colours; the verdict and diagnostic are order-independent",
	"internal/ai/manager.go *Manager.EnsureStrategicInitialized df04ad0a0adf5788480cb3a77e4461881c9681b4a6ede1f7a59e35a29969fa7d":  "gathers catalog keys and sorts them before strategic initialization",
	"internal/ai/manager.go *Manager.EnsureStrategicInitialized 49821edd23bb8678aec944823c688db25591c31d8633a8e6b3070fee6b4f91cf":  "copies independent class-vector values before the maps are rebuilt",
	"internal/ai/manager.go *Manager.EnsureStrategicInitialized 143e739971a876a6dec004cfb0c1eec87a450131b1a8cb5ebf864e7134f914ea":  "restores independent class-vector values by key after deterministic initialization",
	"internal/ai/profile.go cloneWeightTable 3438197ca1a62ee230169a7fdbd1767c419c250ec3ab2636216d11af93b8a1b8":                     "gathers keys and sorts them before copying values",
	"internal/ai/profile.go *Profile.Difficulties 121fcfb90c9967581c05938dd3cedb5d32a90b6dfa7160edb251609c9e38faa9":                "builds a set; later traversal is sorted",
	"internal/ai/profile.go *Profile.Difficulties 89476ddf7b72a0802b9c9b8aceb51d93fa413ec90af9de1b2267d4f60358f64f":                "builds a set; later traversal is sorted",
	"internal/ai/profile.go *Profile.Difficulties acc21e1e3de22ada7c695b0abd96cd6add30d9b0c3abcdea42c236271e260b94":                "gathers difficulty values and sorts them before returning",
	"internal/ai/profile.go LoadProfile 26cc4132c8787df22a9b7f051f9f99e2fea6b5af920a835ac7b9d696de1dcbca":                          "compiled plan keys are canonical one of any/easy/medium/hard; each write is independent by key",
	"internal/ai/profile.go LoadProfile 01b396ee0fe5fb3b2d2beeb2c50472b8b4bd5d667d7c456c2dfe9dfa6e6d5a55":                          "copies independent weight entries from the compiled plan table",
	"internal/ai/profile.go LoadProfile 890604c92b3b039d202cc05f9110eb14b6a39653d7d5eaa62d97de16df76745b":                          "copies independent limit entries from the compiled plan table",
	"internal/ai/profile.go LoadProfile f37ac58ee86dc1f1482c347f6798c1a96f100112a61384f8ad973dc70d05c7fa":                          "gathers fallback candidates and sorts them before choosing",
	"internal/ai/profile.go LoadProfile 0280b14a954a8c2a978ced97a35ae44f5a46001ea4c2be131ed7f087410383e2":                          "gathers fallback candidates and sorts them before choosing",
	"internal/ai/strategic.go *Strategic.InitClassVectors 29779bffdb49550306ca56874236ae6dad68fa86200247860924a2a074100a30":        "builds a key union; all later state changes use its sorted slice",
	"internal/ai/strategic.go *Strategic.InitClassVectors 8fcd02e17c7dda3b0d0567c71d54830e90c9fd280ef197ebe5b1da2051e87e93":        "builds a key union; all later state changes use its sorted slice",
	"internal/ai/strategic.go *Strategic.InitClassVectors e79578d327977829fe121c11424ea67b57c82eac29b688bab842f58aff975676":        "builds a key union; all later state changes use its sorted slice",
	"internal/ai/strategic.go *Strategic.InitClassVectors 18908f679e53e3c7fe9e577e28acb6299128b38a3a14cacbd77646bff7b9c5e7":        "gathers a key union and sorts it before class-vector work",
	"internal/ai/strategic.go *Strategic.refreshCountsAndCenter a051d045678d378db6571dac1ce6ecf5c01d4d653a78570d6dcbdfe0488ef0dc":  "resets independent per-key counters to one constant",
	"internal/ai/strategic.go *Strategic.refreshCountsAndCenter 19c9bf5a91895121468b9dbc6ab2055bb4893f34da93ee33a3dfe2371bb623ea":  "adds absent keys with one constant before ordered unit traversal",
	"internal/ai/strategic.go *Strategic.recomputeClassVectors 8fcd02e17c7dda3b0d0567c71d54830e90c9fd280ef197ebe5b1da2051e87e93":   "builds a key union; all class-vector work uses its sorted slice",
	"internal/ai/strategic.go *Strategic.recomputeClassVectors 9590d1c8637fdc22b9f638f74db726374c799501b11248f120d070556227dd92":   "builds a key union; all class-vector work uses its sorted slice",
	"internal/ai/strategic.go *Strategic.recomputeClassVectors e79578d327977829fe121c11424ea67b57c82eac29b688bab842f58aff975676":   "builds a key union; all class-vector work uses its sorted slice",
	"internal/ai/strategic.go *Strategic.recomputeClassVectors 29779bffdb49550306ca56874236ae6dad68fa86200247860924a2a074100a30":   "builds a key union; all class-vector work uses its sorted slice",
	"internal/ai/strategic.go *Strategic.recomputeClassVectors 18908f679e53e3c7fe9e577e28acb6299128b38a3a14cacbd77646bff7b9c5e7":   "gathers a key union and sorts it before class-vector work",
	"internal/construction/placement.go *Service.BuilderLinks 8466aaa44621ca02916ab416518e813fc183fba49d5412297e13e5a9d8a35fe9":    "returns an independent map copy; it performs no simulation update",
	"internal/construction/placement.go *Service.SnapshotLinks 25a0e243eae77988d06fc0ea4c7529adb42fc0c4bc4a0a0b640eee53edbee946":   "gathers records and sorts product then builder before returning",
	"internal/session/ai_entry.go initializeBattleAI ccc09334e9842e8b751ea2fb6be83c568675169ad3be0e46d82e3aa365396b71":             "gathers catalog keys and sorts them before strategic initialization",
	"internal/session/mission.go pruneRestrictedBuildMenus 7e59e489ce3fe77412e3b63fa6daf9a84cea740f31e48ffa7cc12a8ce2812e4f":       "gathers builder keys and sorts them before rewriting the catalog",
	"internal/aikit/brains/utility/params.go ParseParams 05169ff4b658fc9b5db5f74110b55fdb14e68b2d1a5750d874b6fccd484c4cd0":         "gathers the override names and sorts them before any is applied; it runs when a brain is built from a player spec, never in a think",
}

// mapFunctionHashes bind the enclosing operation as well as its individual
// map range. This makes a sort, callback or other ordering dependency part of
// the audit rather than allowing it to change behind an unchanged range body.
var mapFunctionHashes = map[string]string{
	"internal/cob/checkpoint_ports.go checkpointPortKeys":                              "9010bf2983f1c41445970b452bd46099bb1fdf195d88e65b257cba5cae29c11d",
	"internal/session/checkpoint_scenario.go checkpointScenarioRemovedHandles":         "817165a098c54bd827e6c88f3165e1bd3319f64a1b3430d49b294c1c6b9f3a56",
	"internal/session/checkpoint_visibility.go *Session.writeCheckpointVisibilityTail": "c728ef5c2b0392561eae14e27a02320946276b2496ff32e5619482cb9f8202bd",
	"internal/ai/checkpoint_profile.go checkpointSortedKeys":                           "f4f7ed7d062c24031a97ef9d703ee48fadb66839dc9088b16b0330a8b1804d45",
	"internal/ai/checkpoint_profile.go *Profile.checkpointRecordIDs":                   "f5bdd60fadf54b949803cdc318f4394965540b6c3af53ec794565469b4aee433",
	"internal/movement/checkpoint_layers.go checkpointLayerKeys":                       "0584cfaa241485e03864625160d6cd7c71852c41c188c5320d8ba3156c4270e5",
	"internal/movement/checkpoint_provider.go checkpointRequestKeys":                   "689e4a6a5d33c43a3633d16574f8dd544865e3c9af55bf39b13739c6d25eb0b4",
	"internal/combat/checkpoint.go *Service.checkpointDeathHandles":                    "6872c150476f4a5c4e5d943ff87212f054eac16e999a0a6e880314484caae97d",
	"internal/construction/checkpoint.go checkpointHandleKeys":                         "81c7d44ebe23d4a1ae26e800662d3e5d767d4b8a2a732912931bcc0329d6d0f3",
	"internal/path/checkpoint_write.go checkpointMapEntries":                           "fde409145e1029b92f089b724a3ba224afb12d57375812626930977eac7b22d8",
	"internal/visibility/checkpoint.go *Service.checkpointObserverIDs":                 "8903cc12e8a2656e58db23485e2a7498f62dc42c28045312eb010f01c32c9472",

	// On-demand diagnostic projections copy into local storage only. Pin their
	// sorts and full read operations so future edits require another I1 review.
	"internal/cob/debug_capture.go *VM.DebugSnapshot":            "f19b940e78dceab1d9c60fabb131113f547a21ce600853d5626368935c55d2f1",
	"internal/features/debug_capture.go *Service.DebugSnapshot":  "b88eeadbf744ab1a8e7d0580cf69be24c9dbbba5c567641de4968dc3aa80a6ad",
	"internal/ai/manager.go *Manager.EnsureStrategicInitialized": "cba316ea4945f9095fea37a058826bc5f346f0451711b017b426d5ce2eefa660",
	"internal/ai/profile.go cloneWeightTable":                    "c24030e7c8fcd13b14aeae9ef333f94c571fd0b0b98e59b14a3d7db12c86636e",
	"internal/ai/profile.go *Profile.Difficulties":               "6ebccbe5fdec25f142139476db16d2e8dc5a6ca6df55bbfbf4a21842b8fc10bd",
	"internal/ai/profile.go LoadProfile":                         "119b5500c827867e150450327bd13e543b91f5a0711b8b9df6f7a4eb7fc4708f",
	"internal/ai/strategic.go *Strategic.InitClassVectors":       "22eab29d4bc94cc0442920275dd3e5613c324a5349d32f9880093cf0b854f64f",
	"internal/ai/strategic.go *Strategic.refreshCountsAndCenter": "27025603520a216e18ea26c811992220b05173da9fe49ce7ec544913f7eb2b40",
	// Re-audited after stored predicates and query/coefficient precision fixes [08 R-P0-05 §5].
	// All map ranges only collect a key union; its sorted slice still drives
	// every class-vector calculation and write (I1).
	"internal/ai/strategic.go *Strategic.recomputeClassVectors": "fc4ad28301055ae7a07a283a792f413f30a347d25fe8b4c1d8ed729227f4841e",
	// Re-audited: the piece link now uses retail's slot pass and records link
	// notes [04 R-COB-01 §4]. The two port-install ranges are unchanged and
	// still only install handlers by key (I1).
	"internal/cob/binding.go bindStrict":                        "2a947c14c9ce75fbb8f23deb6f8a48e76b8bea4f9a0fa9db524c66b0742b48f4",
	"internal/construction/placement.go *Service.BuilderLinks":  "ab64326b5234a8e82416673727052e72d47df694047af752dba5b34341f3c0bf",
	"internal/construction/placement.go *Service.SnapshotLinks": "c001891b664d5693829dc524e5c7bad19b8a14c4341d396557013d6a5c1b40ca",
	// Re-audited: rebuilds retain the key/value buffers and clear old pointer
	// tails. Both borrowers finish before the next rebuild. The map range,
	// ascending key sort and parallel value fill remain unchanged (I1).
	"internal/features/service.go *Service.sortedInstanceKeys":    "ca38cb5c735646b0cc2d141ff9ad5f153dc67c3d0948d0976a6e7fda1a066fab",
	"internal/save/battle_image.go validateCarrierReferenceGraph": "3dd0048516ffdf5e47c88f0d813e8ea79da1990b598ab004ebc0fd0f6d031da0",
	// Re-audited: the manager literal now also takes the bound rule set's
	// think step, construction membership rule and the projected ProTA package
	// AI switches, and the manager copies the battle seed for a Modern
	// controller's private generator. The two sight predicates are session
	// methods (the retail rally one and the Modern controller's own-coverage
	// one). The jammer predicate delegates to the session's bound visibility
	// service and introduces no iteration. The catalog-key union, its sort and
	// the order every consumer sees are unchanged (I1).
	"internal/session/ai_entry.go initializeBattleAI": "fefdb3ef55acccd84956d4a8cfdec7998a8eb4f0c9f8dc7e7b213ab36ea73039",
	// Re-audited: the immutable membership lists and the installed products
	// are filtered after the same sorted builder-key walk; each list is a
	// slice walk in its own order, and no map-order-dependent decisions were
	// added.
	"internal/session/mission.go pruneRestrictedBuildMenus": "166474bda03242d6710515c18671e3b6da60c12176c40c6a7ac2006e535151b4",
	// A Modern AI brain's parameter overrides, applied in sorted name order
	// when the brain is built; a think never reaches it (I1).
	"internal/aikit/brains/utility/params.go ParseParams":         "4e69b3d510bbaf5e0ae6ad1d6ef0e39443eae75eda7d248adc2a29c386de3e19",
	"internal/units/cob_binding.go bindUnitPortHandlers":          "31dfa18e2683e5bec165669f55e4f5dad260588513831504622f3b1596ba5548",
	"internal/units/cob_binding.go bindCOBWithPortsAndVisibility": "d1d6860864e573f34a1a654d7776da50141e3717b53f603b93b97e5447d26e6f",
}

func checkAuthoritativeTypedMapRanges(t *testing.T) {
	t.Helper()
	root := repositoryRoot(t)
	failures := mapRangeViolations(typedMapRanges(root, loadAuthoritativeTypedPackages(t, root)), mapRangeExceptions, mapFunctionHashes)
	if len(failures) != 0 {
		sort.Strings(failures)
		t.Fatalf("map iteration (I1) guard failed:\n%s", strings.Join(failures, "\n"))
	}
}

func mapRangeViolations(sites []typedMapRange, rangeExceptions, functionHashes map[string]string) []string {
	seenRanges := map[string]bool{}
	seenFunctions := map[string]bool{}
	var failures []string
	for _, site := range sites {
		key := site.key()
		if _, ok := rangeExceptions[key]; !ok {
			failures = append(failures, fmt.Sprintf("%s:%d: unreviewed map range in %s; key %q; body %s", site.path, site.line, site.function, key, site.body))
			continue
		}
		seenRanges[key] = true
		functionKey := site.functionKey()
		expected, ok := functionHashes[functionKey]
		if !ok {
			failures = append(failures, fmt.Sprintf("%s:%d: map range has no function audit for %q; audit it and pin %s", site.path, site.line, functionKey, site.functionHash))
			continue
		}
		seenFunctions[functionKey] = true
		if site.functionHash != expected {
			failures = append(failures, fmt.Sprintf("%s:%d: audited map function changed: %s; re-audit it and pin %s", site.path, site.line, functionKey, site.functionHash))
		}
	}
	for key := range rangeExceptions {
		if !seenRanges[key] {
			failures = append(failures, "stale map-range exception: "+key)
		}
	}
	for key := range functionHashes {
		if !seenFunctions[key] {
			failures = append(failures, "stale map-function exception: "+key)
		}
	}
	return failures
}

type float64Allowance struct {
	count  int
	reason string
}

// float64FieldAllowances names the binary64 storage fields found in package-level
// named struct declarations. The key retains the declaration and authored type syntax,
// so grouped fields and named or aliased float types cannot hide new state.
var float64FieldAllowances = map[string]string{
	"internal/ai/selection.go type ScoreInputs.NetEnergy float64": "I2 transient net-query return retains working precision through comparison [08 R-P0-05 §3]",
	"internal/ai/selection.go type ScoreInputs.NetMetal float64":  "I2 transient net-query return retains working precision through comparison [08 R-P0-05 §3]",
	// Audited model rotation coefficients: cached draw/admission transform trig,
	// never new simulation state; geometry narrows after each rotation [I2].
	"internal/model/model.go type xformNode.cx float64": "I2 model piece rotation trig cache [03 §2.4]",
	"internal/model/model.go type xformNode.sx float64": "I2 model piece rotation trig cache [03 §2.4]",
	"internal/model/model.go type xformNode.cy float64": "I2 model piece rotation trig cache [03 §2.4]",
	"internal/model/model.go type xformNode.sy float64": "I2 model piece rotation trig cache [03 §2.4]",
	"internal/model/model.go type xformNode.cz float64": "I2 model piece rotation trig cache [03 §2.4]",
	"internal/model/model.go type xformNode.sz float64": "I2 model piece rotation trig cache [03 §2.4]",
	"internal/model/model.go type trigNode.cx float64":  "I2 model piece rotation trig cache [03 §2.4]",
	"internal/model/model.go type trigNode.sx float64":  "I2 model piece rotation trig cache [03 §2.4]",
	"internal/model/model.go type trigNode.cy float64":  "I2 model piece rotation trig cache [03 §2.4]",
	"internal/model/model.go type trigNode.sy float64":  "I2 model piece rotation trig cache [03 §2.4]",
	"internal/model/model.go type trigNode.cz float64":  "I2 model piece rotation trig cache [03 §2.4]",
	"internal/model/model.go type trigNode.sz float64":  "I2 model piece rotation trig cache [03 §2.4]",

	"internal/economy/ledger.go type Player.Waste [2]float64":                        "I2 cumulative waste counters [05 \"Stocks, counters, and waste\"]",
	"internal/economy/ledger.go type Player.TotalProduced [2]float64":                "I2 cumulative totals [05 \"Stocks, counters, and waste\"]",
	"internal/economy/ledger.go type Player.TotalConsumed [2]float64":                "I2 cumulative totals [05 \"Stocks, counters, and waste\"]",
	"internal/economy/p28_parity_trace.go type PlayerTrace.Waste [2]float64":         "I2 opt-in trace copy of cumulative waste (P28-OBS-00C)",
	"internal/economy/p28_parity_trace.go type PlayerTrace.TotalProduced [2]float64": "I2 opt-in trace copy of cumulative totals (P28-OBS-00C)",
	"internal/economy/p28_parity_trace.go type PlayerTrace.TotalConsumed [2]float64": "I2 opt-in trace copy of cumulative totals (P28-OBS-00C)",

	"internal/mission/mission_globals.go type MissionGlobals.TidalStrength float64": "I2 immutable authored map-global value [02 map-global keys]",
	"internal/mission/mission_globals.go type MissionGlobals.KillMul float64":       "I2 immutable authored map-global value [02 map-global keys]",
	"internal/mission/mission_globals.go type MissionGlobals.TimeMul float64":       "I2 immutable authored map-global value [02 map-global keys]",
	"internal/save/bank.go type DoubleItem.Value float64":                           "I13 HAPIBANK account record double",
	"internal/save/boxes.go type PlayerSlot.TotalEnergyProduced float64":            "I2/I13 player save-box double",
	"internal/save/boxes.go type PlayerSlot.TotalMetalProduced float64":             "I2/I13 player save-box double",
	"internal/save/boxes.go type PlayerSlot.TotalEnergyConsumed float64":            "I2/I13 player save-box double",
	"internal/save/boxes.go type PlayerSlot.TotalMetalConsumed float64":             "I2/I13 player save-box double",
	"internal/save/boxes.go type PlayerSlot.EnergyWasted float64":                   "I2/I13 player save-box double",
	"internal/save/boxes.go type PlayerSlot.MetalWasted float64":                    "I2/I13 player save-box double",
}

// float64ScopeAllowances is intentionally declaration-scoped. Each entry
// names the precise retail operation that needs binary64; all other float64
// occurrences still use the shrink-only per-file baseline.
var float64ScopeAllowances = map[string]float64Allowance{
	"internal/economy/checkpoint.go func writeCheckpointF64Pair":       {1, "I2 exact stored ledger binary64 totals and waste forwarded to canonical bit encoding; no floating arithmetic (DESIGN_MULTIPLAYER §16.3.13)"},
	"internal/sim/checkpoint/encoder.go func *Encoder.F64":             {1, "I2 exact stored binary64 bits for canonical diagnostics; no floating arithmetic (DESIGN_MULTIPLAYER §16.3.6)"},
	"internal/orders/scans.go func patrolResourceAtLeastTwenty":        {3, "I2 patrol stored stock/capacity and binary64 threshold constant compared at working precision [04 R-ORD-01 §4]"},
	"internal/orders/scans.go func resourceFits":                       {3, "I2 patrol stored stock plus stored feature value compared without narrowing [04 R-ORD-01 §4]"},
	"internal/economy/share.go func *Service.shareSeatResource":        {7, "I2 automatic-share ratios, and excess, gap and product compared at working precision [05 R-SHARE-01 §3][01 §8]"},
	"internal/ai/strategic.go func classify":                           {8, "I2 net-energy query stored inputs and working-precision return [05 R-PROD-01 §1]"},
	"internal/ai/strategic.go func *Strategic.classify":                {1, "I2 net-energy query result forwarded without a single store [05 R-PROD-01 §1]"},
	"internal/ai/strategic.go func *Strategic.recomputeClassVectors":   {25, "I2 class coefficients retain working precision between explicit single stores [08 R-P0-05 §5]"},
	"internal/ai/strategic.go func ftol64":                             {1, "I2 class working value reaches the truncating integer conversion [08 R-P0-05 §5]"},
	"internal/orders/combat.go func leashBroken":                       {2, "I2 signed whole-unit leash distance through retail helper [04 R-STANCE-01 §4][01 R-DET-01 §7]"},
	"internal/orders/work.go func footprintPad":                        {2, "I2 signed footprint words through retail distance before scaling and truncation [05 R-WORK-01 §2]"},
	"internal/orders/work.go func inBuildRange":                        {2, "I2 wrapping raw coordinate differences through retail distance before signed high-word read [05 R-WORK-01 §2]"},
	"internal/construction/factory.go func nanoFootprintPad":           {2, "I2 signed footprint words through retail distance before scaling and truncation [05 R-WORK-01 §2]"},
	"internal/construction/factory.go func *Service.isWithinNanoRange": {2, "I2 wrapping raw coordinate differences through retail distance before signed high-word read [05 R-WORK-01 §2]"},
	"internal/movement/airorders.go func airPlanarDistance":            {2, "I2 wrapping raw coordinate differences through retail distance before signed low-word truncation [04 R-AIR-01 §8]"},

	"internal/effects/fragment.go func buildFragmentGeometry": {3, "I2 stored binary32 normal scaled for signed-low-word extrusion and velocity conversion [04 R-COB-04 §3]"},
	"internal/effects/fragment.go func fragmentNormal":        {42, "I2 shatter normal differences, exact binary32-widened products, square root and division; named components narrow to binary32 [04 R-COB-04 §3]"},
	"internal/effects/fragment.go const fragmentVertexScale":  {1, "I2 authored binary32 reciprocal-65535 coordinate conversion [04 R-COB-04 §3]"},
	"internal/effects/fragment.go func fragmentVertexFloat":   {2, "I2 retained vertex coordinate multiplied by the stored binary32 reciprocal, then narrowed to binary32 [04 R-COB-04 §3]"},

	"internal/construction/community_kickout.go const kickoutPi,kickoutQuarterPi":     {2, "I2 sourced Community kickout angle constants [CP-CON-1]"},
	"internal/construction/community_kickout.go func CommunityRules.YieldObstruction": {2, "I2 sourced Community kickout random bearing [CP-CON-1]"},
	"internal/construction/community_kickout.go func *Service.shouldKickout":          {4, "I2 sourced Community kickout invested-energy test [CP-CON-1]"},
	"internal/construction/community_kickout.go func *Service.findKickoutDestination": {29, "I2 sourced Community kickout bearing and sweep geometry [CP-CON-1]"},
	"internal/construction/community_kickout.go func forwardCircleIntersection":       {39, "I2 sourced Community kickout tangent-branch circle/line geometry [CP-CON-1]"},
	"internal/construction/community_kickout.go func kickoutQuadratic":                {10, "I2 sourced Community kickout quadratic root operation order [CP-CON-1]"},
	"internal/construction/community_repair.go func communityRepairEnergy":            {4, "I2 Community repair energy helper working precision [CP-DMG-4]"},
	"internal/model/model.go func applyChain":                                         {18, "I2 model vertex working precision; round after each axis then narrow geometry [03 §2.4]"},

	"internal/combat/meteor.go func MeteorDelay":               {2, "I2 meteor source float32, working quotient and signed64/low32 conversion [06 §6.5][01 R-DET-01 §1]"},
	"internal/combat/meteor.go func MeteorDurationTicks":       {2, "I2 meteor source float32, working product and signed64/low32 conversion [06 §6.5][01 R-DET-01 §1]"},
	"internal/combat/meteor.go func MeteorIntervalTicks":       {2, "I2 meteor source float32, working product and signed64/low32 conversion [06 §6.5][01 R-DET-01 §1]"},
	"internal/session/step.go func *Session.initMeteor":        {3, "I2 selected-schema meteor source stores enter the documented working-precision conversion helpers [06 §6.5][01 R-DET-01 §1]"},
	"internal/clock/clock.go func *State.budget":               {5, "I2 clock budget product and float32 carry [01 §4.2]"},
	"internal/clock/clock.go func *State.effectiveSpeedLocked": {3, "I2 clock speed multiplier [01 §4.2]"},
	"internal/clock/clock.go func lagThrottleFactor":           {6, "I2 retained multiplayer throttle expression [01 §4.2]"},
	"internal/clock/clock.go func decodeBox":                   {2, "I2 clock save-box float32 validation [01 §4.2]"},

	"internal/ai/selection.go func energyRaw":              {6, "I2 candidate-score energy pressure: the capacity difference and its product at retail's 53-bit working precision, narrowed only by the two named truncations [08 R-P0-05 §3][08 \"Arithmetic and clamping\"]"},
	"internal/ai/selection.go func ScoreInputsFromEconomy": {4, "I2 stored production and consumption widened before the net subtraction [08 R-P0-05 §3]"},
	"internal/ai/selection.go func metalRaw":               {6, "I2 candidate-score metal pressure: the capacity difference and its product at retail's 53-bit working precision, narrowed only by the two named truncations [08 R-P0-05 §3][08 \"Arithmetic and clamping\"]"},

	"internal/economy/admission.go func settlePure":                   {12, "I2 settlement working precision [05 R-ECO-01 §1][05 R-ECO-01 §5]"},
	"internal/economy/admission.go func *Service.settleOneResource":   {12, "I2 settlement working precision [05 R-ECO-01 §1][05 R-ECO-01 §5]"},
	"internal/economy/ledger.go func rebuildCapacityPlayer":           {4, "I2 economy capacity working precision [05 R-ECO-01 §1]"},
	"internal/economy/ledger.go func debitCloakToBucket":              {4, "I2 economy working precision [05 R-ECO-01 §1]"},
	"internal/economy/ledger.go func *Player.commitPassCounters":      {2, "I2 cumulative counters [05 \"Stocks, counters, and waste\"]"},
	"internal/economy/ledger.go func *Player.commitCapacityWaste":     {2, "I2 waste counter [05 \"Stocks, counters, and waste\"]"},
	"internal/economy/ledger.go func ImmediateDebit":                  {8, "I2 economy working precision [05 R-ECO-01 §1]"},
	"internal/economy/ledger.go func *Service.transfer":               {5, "I2 economy working precision [05 R-ECO-01 §1]"},
	"internal/economy/maker.go func addContribution":                  {9, "I2 production contributions and discounts [05 R-ECO-01 §1][05 R-ECO-01 §3]"},
	"internal/economy/maker.go func *Service.PerUnitProductionFills":  {9, "I2 production contributions [05 R-ECO-01 §1][05 R-ECO-01 §3]"},
	"internal/economy/maker.go func creditReclaimedMaterial":          {9, "I2 reclaimed-material accounting [05 R-ECO-01 §1]"},
	"internal/economy/maker.go func *Service.CreditFeatureReclaim":    {4, "I2 reclaimed-material accounting [05 R-ECO-01 §1]"},
	"internal/economy/maker.go func proTAIncomeCredit":                {8, "I2 ProTA 4.8 package income credit at the retail working precision [05 R-ECO-01 §1][05 R-ECO-01 §3]"},
	"internal/economy/maker.go func *Service.CreditUnitReclaimRefund": {2, "I2 construction refund [05 R-ECO-01 §11]"},

	"internal/movement/airorders.go func airReleaseLead":  {5, "I2 AirStrike release lead [04 R-AIR-01 §8]"},
	"internal/movement/flight.go func flightGoalDistance": {2, "I2 flight goal distance [04 R-AIR-01 §1]"},
	"internal/movement/flight.go func rotateLeanPair":     {8, "I2 lean rotation [04 R-AIR-01 §2]"},
	"internal/movement/flight.go func IntegrateFlight":    {34, "I2 flight braking and integration temporaries [04 §10.1]"},
	"internal/movement/integrate.go func groundHypotRaw":  {2, "I2 ground follower route distance [04 R-MOV-01 §3]"},

	"internal/combat/motion.go func InitOrdinary":            {2, "I2 ordinary creator stored planar distance [06 §6.3][06 §4.3]"},
	"internal/combat/aim.go func BallisticSolve":             {26, "I2 ballistic discriminant, acos, sqrt and two researched angle literals [06 §3.3]"},
	"internal/combat/aim.go func distance3DRaw":              {6, "I2 pre-fire lead distance [06 §3.3]"},
	"internal/combat/impact.go func DistanceToBox":           {6, "I2 area-damage range [06 §9.3]"},
	"internal/combat/damage.go func Falloff":                 {4, "I2 area-damage falloff [06 §9.3]"},
	"internal/combat/damage.go func weaponNominal":           {2, "I2 area-damage amount product [06 §9.2]"},
	"internal/construction/reclaim.go func UnitReclaimPulse": {2, "I2 unit-reclaim pulse divide [05 R-WORK-01 §4]"},

	"internal/survival/pool.go func *Pool.Cost":               {2, "I2 exact float32 widening into defined signed-64 cost conversion (DESIGN_SURVIVAL §5)"},
	"internal/survival/pool.go func buildPool":                {2, "I2 exact float32 widening into defined signed-64 ratio conversion (DESIGN_SURVIVAL §5)"},
	"internal/session/survival.go func *Session.stepSurvival": {1, "I2 exact float32 widening into defined signed-64 reward text conversion (DESIGN_SURVIVAL §6.9)"},

	"internal/sim/numeric/angletable.go var angleSinCos":          {4, "I2 immutable binary64 angle table constructed with the existing angle expression (DESIGN_MULTIPLAYER §16.1 M1-C3)"},
	"internal/sim/numeric/angletable.go func AngleSinCos":         {1, "I2 immutable binary64 angle table lookup (DESIGN_MULTIPLAYER §16.1 M1-C3)"},
	"internal/sim/numeric/convert.go func TruncateFloat64ToInt64": {3, "I2/I3 signed-64 truncation and invalid-result bounds [01 R-DET-01 §1]"},
	"internal/sim/numeric/convert.go func RoundFloat64ToInt32":    {3, "I2 nearest-even signed-32 conversion and invalid-result bounds [01 R-DET-01 §2]"},
	"internal/sim/numeric/distance.go func decompose":             {1, "I2 exact binary64 operand decomposition for integer distance arithmetic [01 R-DET-01 §7]"},
	"internal/sim/numeric/distance.go func store":                 {1, "I2 binary64 store boundary after integer significand rounding [01 R-DET-01 §7]"},
	"internal/sim/numeric/distance.go func root":                  {1, "I2 stored sum input to the integer extended-precision root [01 R-DET-01 §7]"},
	"internal/sim/numeric/distance.go func sqrt128":               {5, "I2 bounded binary64 root estimate corrected to an exact integer root [01 R-DET-01 §7]"},
	"internal/sim/numeric/distance.go func Distance":              {2, "I2 binary64 operand/result slots around the integer retail rounding sequence [01 R-DET-01 §7]"},
	"internal/sim/numeric/distance.go func TruncatedDistance":     {7, "I2 proven error-bound shortcut with retail-sequence fallback [01 R-DET-01 §7] (DESIGN_MULTIPLAYER §16.1 M1-C1)"},
	"internal/sim/numeric/radians.go var _sin":                    {7, "I2 immutable sine approximation coefficients from the approved Go adaptation (DESIGN_MULTIPLAYER §16.1 M1-C2)"},
	"internal/sim/numeric/radians.go var _cos":                    {7, "I2 immutable cosine approximation coefficients from the approved Go adaptation (DESIGN_MULTIPLAYER §16.1 M1-C2)"},
	"internal/sim/numeric/radians.go func CosRadians":             {29, "I2 unfused cosine reduction and polynomial working precision (DESIGN_MULTIPLAYER §16.1 M1-C2)"},
	"internal/sim/numeric/radians.go func SinRadians":             {29, "I2 unfused sine reduction and polynomial working precision (DESIGN_MULTIPLAYER §16.1 M1-C2)"},
	"internal/sim/numeric/radians.go var _tanP":                   {4, "I2 immutable tangent numerator coefficients from the approved Go adaptation (DESIGN_MULTIPLAYER §16.1 M1-C2)"},
	"internal/sim/numeric/radians.go var _tanQ":                   {6, "I2 immutable tangent denominator coefficients from the approved Go adaptation (DESIGN_MULTIPLAYER §16.1 M1-C2)"},
	"internal/sim/numeric/radians.go func TanRadians":             {20, "I2 unfused tangent reduction and rational approximation working precision (DESIGN_MULTIPLAYER §16.1 M1-C2)"},
	"internal/sim/numeric/radians.go func xatan":                  {23, "I2 unfused arctangent rational approximation working precision (DESIGN_MULTIPLAYER §16.1 M1-C2)"},
	"internal/sim/numeric/radians.go func satan":                  {6, "I2 arctangent range reduction and reconstruction (DESIGN_MULTIPLAYER §16.1 M1-C2)"},
	"internal/sim/numeric/radians.go func atan":                   {2, "I2 arctangent sign handling around the portable approximation (DESIGN_MULTIPLAYER §16.1 M1-C2)"},
	"internal/sim/numeric/radians.go func Atan2Radians":           {2, "I2 quadrant selection around the portable arctangent (DESIGN_MULTIPLAYER §16.1 M1-C2)"},
	"internal/sim/numeric/radians.go func asin":                   {4, "I2 unfused arcsine reduction through the correctly rounded library root and portable arctangent (DESIGN_MULTIPLAYER §16.1 M1-C2)"},
	"internal/sim/numeric/radians.go func AcosRadians":            {2, "I2 arccosine reconstruction from portable arcsine (DESIGN_MULTIPLAYER §16.1 M1-C2)"},
	"internal/sim/numeric/radians.go func trigReduce":             {3, "I2 Payne-Hanek reduction returning binary64 radians after integer reduction (DESIGN_MULTIPLAYER §16.1 M1-C2)"},

	"internal/sim/numeric/numeric.go func TruncateFloat32ToLow32": {1, "I2 exact widening of stored single precision into the shared I3 conversion [01 R-DET-01 §1]"},
	"internal/sim/numeric/numeric.go func TruncateFloat64ToLow32": {3, "I2 authored conversion and I3 signed-low-word narrowing [01 R-DET-01 §1]"},
	"internal/sim/numeric/trig.go const angleScale":               {1, "I2 simulation trig-table construction [04 §5.1]"},
	"internal/sim/numeric/trig.go func AngleFromAtan2":            {2, "I2 simulation trig-table construction [04 §5.1]"},
	"internal/sim/numeric/trig.go func init":                      {1, "I2 simulation trig-table construction [04 §5.1]"},

	"internal/save/boxes.go func WritePlayerSlot":   {4, "I2/I13 player save-box doubles"},
	"internal/save/boxes.go func ReadPlayerSlot":    {2, "I2/I13 player save-box doubles"},
	"internal/save/bank.go func *Account.SetDouble": {1, "I13 HAPIBANK account record double"},
	"internal/save/bank.go func *Account.Double":    {1, "I13 HAPIBANK account record double"},

	"internal/session/strips.go func nanoLifetimeTicks": {7, "I2 nanolathe particle travel distance [03 §5.5]"},
	"internal/session/strips.go func sprinkleStep":      {9, "I2 strip-object span [03 R-FX-01 §3]"},
	"internal/session/strips.go func flameSegLife":      {9, "I2 strip-object span [03 R-FX-02 §2]"},

	"internal/orders/work.go const captureCostScale,captureEnergyUnit,captureMetalUnit,captureBias,captureClampMax": {4, "I2 capture timer constants [05 R-WORK-01 §6]"},
	"internal/orders/work.go func captureBudget":     {9, "I2 capture timer base sum [05 R-WORK-01 §6]"},
	"internal/orders/work.go func resurrectionDelay": {3, "I2 resurrection delay [05 R-WORK-01 §7]"},
}

func checkAuthoritativeFloat64(t *testing.T) {
	t.Helper()
	checkAuthoritativeFloat64Fields(t)
	current := map[string]int{}
	sites := map[string][]occurrence{}
	seen := map[string]bool{}
	var failures []string
	for _, scope := range float64Scopes(t) {
		if allowance, ok := float64ScopeAllowances[scope.key()]; ok {
			seen[scope.key()] = true
			if violation := float64ScopeViolation(scope, allowance); violation != "" {
				failures = append(failures, violation)
			}
			continue
		}
		current[scope.path] += scope.count
		sites[scope.path] = append(sites[scope.path], scope.sites...)
	}
	for key, allowance := range float64ScopeAllowances {
		if !seen[key] {
			failures = append(failures, fmt.Sprintf("stale float64 scope allowance %q (%s)", key, allowance.reason))
		}
	}
	failures = append(failures, enforceRatchet(t, "float64 (I2)", float64Baseline, current, sites)...)
	if len(failures) != 0 {
		sort.Strings(failures)
		t.Fatalf("float64 (I2) guard failed:\n%s", strings.Join(failures, "\n"))
	}
}

func checkAuthoritativeFloat64Fields(t *testing.T) {
	t.Helper()
	root := repositoryRoot(t)
	failures := float64FieldViolations(typedFloat64Fields(root, loadAuthoritativeTypedPackages(t, root)), float64FieldAllowances)
	if len(failures) != 0 {
		sort.Strings(failures)
		t.Fatalf("float64 fields (I2) guard failed:\n%s", strings.Join(failures, "\n"))
	}
}

func float64FieldViolations(fields []typedFloat64Field, allowances map[string]string) []string {
	seen := map[string]bool{}
	var failures []string
	for _, field := range fields {
		if _, ok := allowances[field.key]; !ok {
			failures = append(failures, fmt.Sprintf("%s:%d: unreviewed float64 field %q", field.path, field.line, field.key))
			continue
		}
		seen[field.key] = true
	}
	for key := range allowances {
		if !seen[key] {
			failures = append(failures, "stale float64 field allowance: "+key)
		}
	}
	return failures
}

func float64ScopeViolation(scope floatScope, allowance float64Allowance) string {
	if scope.count == allowance.count {
		return ""
	}
	return fmt.Sprintf("float64 scope %q changed %d -> %d (%s); sites %s", scope.key(), allowance.count, scope.count, allowance.reason, formatSites(scope.sites))
}

func formatSites(sites []occurrence) string {
	parts := make([]string, 0, len(sites))
	for _, site := range sites {
		parts = append(parts, fmt.Sprintf("%d:%s", site.line, site.text))
	}
	return strings.Join(parts, "; ")
}

func TestTypedMapRangeDetectorFindsMapExpressions(t *testing.T) {
	root := t.TempDir()
	const source = `package fixture
type named map[int]int
type holder struct { values map[int]int }
func returned() map[int]int { return nil }
func ranges(h holder) {
	local := map[int]int{}
	for range local {}
	for range h.values {}
	var n named
	for range n {}
	for range returned() {}
	keys := []int{1, 2}
	for range keys {}
}`
	pkg := typedFixture(t, filepath.Join(root, "fixture.go"), source)
	ranges := typedMapRanges(root, []typedPackage{pkg})
	if len(ranges) != 4 {
		t.Fatalf("typed map ranges = %d, want 4: %#v", len(ranges), ranges)
	}
	for _, site := range ranges {
		if !strings.Contains(site.function, "ranges") {
			t.Fatalf("map range attributed to %q, want ranges", site.function)
		}
	}
}

func TestMapRangeAuditRejectsChangedOrderingFunction(t *testing.T) {
	root := t.TempDir()
	const audited = `package fixture
func sortKeys([]int) {}
func sorted(m map[int]int) {
	keys := make([]int, 0, len(m))
	for key := range m { keys = append(keys, key) }
	sortKeys(keys)
}`
	const changed = `package fixture
func sortKeys([]int) {}
func sorted(m map[int]int) {
	keys := make([]int, 0, len(m))
	for key := range m { keys = append(keys, key) }
	_ = keys
}`
	baseline := typedMapRanges(root, []typedPackage{typedFixture(t, filepath.Join(root, "fixture.go"), audited)})
	if len(baseline) != 1 {
		t.Fatalf("audited fixture map ranges = %d, want 1", len(baseline))
	}
	allowedRanges := map[string]string{baseline[0].key(): "fixture sorts gathered keys"}
	allowedFunctions := map[string]string{baseline[0].functionKey(): baseline[0].functionHash}
	if got := mapRangeViolations(baseline, allowedRanges, allowedFunctions); len(got) != 0 {
		t.Fatalf("audited fixture failed: %s", strings.Join(got, "; "))
	}
	changedRanges := typedMapRanges(root, []typedPackage{typedFixture(t, filepath.Join(root, "fixture.go"), changed)})
	if got := mapRangeViolations(changedRanges, allowedRanges, allowedFunctions); len(got) == 0 {
		t.Fatal("removed sort silently passed map-range audit")
	}
}

func TestTypedMapRangeDetectorFindsPackageInitializerClosure(t *testing.T) {
	root := t.TempDir()
	const source = `package fixture
var initialized = func() int {
	values := map[int]int{1: 1}
	for range values {}
	return 0
}()
`
	ranges := typedMapRanges(root, []typedPackage{typedFixture(t, filepath.Join(root, "fixture.go"), source)})
	if len(ranges) != 1 || !strings.HasPrefix(ranges[0].function, "package-init closure@") {
		t.Fatalf("initializer map ranges = %#v, want one package-init closure", ranges)
	}
}

func TestFloat64FieldAuditRejectsGroupedAndNamedFields(t *testing.T) {
	root := t.TempDir()
	const before = `package fixture
type record struct { Allowed float64 }
`
	const after = `package fixture
type scalar float64
type record struct {
	Allowed, Unsafe float64
	Aliased scalar
	Values []float64
	Nested struct { Deep []float64 }
	scalar
}
`
	baseline := typedFloat64Fields(root, []typedPackage{typedFixture(t, filepath.Join(root, "fixture.go"), before)})
	if len(baseline) != 1 {
		t.Fatalf("fixture baseline fields = %d, want 1", len(baseline))
	}
	allowances := map[string]string{baseline[0].key: "fixture's one authorized field"}
	afterFields := typedFloat64Fields(root, []typedPackage{typedFixture(t, filepath.Join(root, "fixture.go"), after)})
	if got := float64FieldViolations(afterFields, allowances); len(got) != 5 {
		t.Fatalf("float field shape violations = %d, want 5: %s", len(got), strings.Join(got, "; "))
	}
}

func TestFloat64FieldAuditChecksContainersAndTerminatesCycles(t *testing.T) {
	root := t.TempDir()
	const source = `package fixture
type tree []tree
type leaves []struct { Next leaves; Value float64 }
type scalar float64
type record struct {
	Pointer *float64
	Map map[int]scalar
	Keys map[float64]int
	Nested []*struct { Value float64 }
	Channel chan scalar
	Recursive leaves
	Empty tree
	Callback func(float64) float64
}
`
	fields := typedFloat64Fields(root, []typedPackage{typedFixture(t, filepath.Join(root, "fixture.go"), source)})
	violations := float64FieldViolations(fields, nil)
	for _, name := range []string{"Pointer", "Map", "Keys", "Nested", "Channel", "Recursive"} {
		found := false
		for _, violation := range violations {
			found = found || strings.Contains(violation, "record."+name+" ")
		}
		if !found {
			t.Errorf("missing violation for %s: %v", name, violations)
		}
	}
	if len(violations) != 6 {
		t.Fatalf("container field violations = %v, want six numeric storage fields", violations)
	}
}

func TestFloat64FieldAuditDoesNotPromotePointerTargetFields(t *testing.T) {
	root := t.TempDir()
	const source = `package fixture
type owner struct { Value float64 }
type record struct { Owner *owner }
`
	fields := typedFloat64Fields(root, []typedPackage{typedFixture(t, filepath.Join(root, "fixture.go"), source)})
	if len(fields) != 1 {
		t.Fatalf("pointer fixture fields = %#v, want one owner field", fields)
	}
	if got, want := fields[0].key, "fixture.go type owner.Value float64"; got != want {
		t.Fatalf("pointer fixture key = %q, want %q", got, want)
	}
}

func TestFloat64ScopeAuditKeepsNonStructTypeDeclarations(t *testing.T) {
	root := t.TempDir()
	pkg := typedFixture(t, filepath.Join(root, "fixture.go"), `package fixture
type scalar float64
`)
	decl := pkg.files[0].Decls[0].(*ast.GenDecl)
	typeSpec := decl.Specs[0].(*ast.TypeSpec)
	scope, ok := float64ScopeForNode("fixture.go", pkg.fset, func(int) string { return "type scalar float64" }, "type scalar", typeSpec)
	if !ok || scope.count != 1 {
		t.Fatalf("non-struct type scope = %#v, present = %t; want one float64 token", scope, ok)
	}
}

func typedFixture(t *testing.T, filename, source string) typedPackage {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, source, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	info := &types.Info{
		Defs:  map[*ast.Ident]types.Object{},
		Uses:  map[*ast.Ident]types.Object{},
		Types: map[ast.Expr]types.TypeAndValue{},
	}
	if _, err := (&types.Config{}).Check("fixture", fset, []*ast.File{file}, info); err != nil {
		t.Fatalf("type-check fixture: %v", err)
	}
	return typedPackage{importPath: "fixture", fset: fset, files: []*ast.File{file}, info: info}
}

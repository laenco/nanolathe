//go:build retail

package profiles_test

import (
	"os"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/content/profiles"
	"github.com/nanolathe-gg/nanolathe/internal/testsupport"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

// modRootsEnvPrefix is followed by the content set's upper-case name. The
// value is a host path list — the mod's own roots in load order, appended
// after the retail install. The paths are never committed: this check skips when the
// variable is unset, which is how it behaves on every machine but the one that
// has the content.
//
//	NANOLATHE_MOD_ROOTS_ESCALATION="<esc install files>"
//	NANOLATHE_MOD_ROOTS_PROTA="<ProTA root>"
//	NANOLATHE_MOD_ROOTS_ZERO="<TA Zero base>:<TA Zero alpha>"
const modRootsEnvPrefix = "NANOLATHE_MOD_ROOTS_"

func modRoots(t *testing.T, profile string) []string {
	t.Helper()
	name := modRootsEnvPrefix + strings.ToUpper(profile)
	value := os.Getenv(name)
	if strings.TrimSpace(value) == "" {
		t.Skipf("%s is unset: this check needs the %s content set's roots in load order", name, profile)
	}
	var roots []string
	for _, root := range strings.Split(value, string(os.PathListSeparator)) {
		if strings.TrimSpace(root) != "" {
			roots = append(roots, root)
		}
	}
	if len(roots) == 0 {
		t.Fatalf("%s=%q names no root", name, value)
	}
	return roots
}

// mountWithMod mounts the retail install and then the mod's roots in load
// order, the overlay a player of that content set runs.
func mountWithMod(t *testing.T, profile string) *vfs.FS {
	t.Helper()
	roots := append([]string{testsupport.RetailRoot(t)}, modRoots(t, profile)...)
	fs := vfs.New()
	t.Cleanup(func() { fs.Close() })
	if err := fs.MountGameDirectories(roots); err != nil {
		t.Fatalf("mount %s roots: %v", profile, err)
	}
	return fs
}

// modContent is the content section of the content set's repository config,
// after checking the mounted content presents every tree its markers name.
func modContent(t *testing.T, fs vfs.FSOps, dir string) profiles.Profile {
	t.Helper()
	profile := shippedContent(t, dir)
	if missing := profile.MissingMarkers(fs); len(missing) != 0 {
		t.Fatalf("the mounted content lacks the %s config's trees %v", dir, missing)
	}
	return profile
}

// unitDefinitionFiles counts the unit definitions the profile's directory
// table makes visible under the retail name. It is the measurement that does
// not depend on the definition domain or the read caps, so it holds for all
// three content sets while E2 and E3 are outstanding.
func unitDefinitionFiles(t *testing.T, view vfs.FSOps) int {
	t.Helper()
	entries, err := view.ReadDir("units")
	if err != nil {
		t.Fatalf("ReadDir units through the directory table: %v", err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir || !strings.HasSuffix(strings.ToLower(entry.Name), ".fbi") {
			continue
		}
		count++
		if !strings.HasPrefix(entry.Path, "units/") {
			t.Fatalf("entry path %q kept the content set's own directory name", entry.Path)
		}
	}
	return count
}

// TestZeroContentSetCompilesThroughItsDirectoryTable is the end-to-end proof
// on real content: TA Zero's trees are all renamed, and with its config's
// table in place the whole catalog compiles, at the definition count the
// inventory recorded, with retail-named provenance.
func TestZeroContentSetCompilesThroughItsDirectoryTable(t *testing.T) {
	fs := mountWithMod(t, "zero")
	profile := modContent(t, fs, "ta-zero-alpha5-20241224")
	view := profile.Layout().Apply(fs)
	if got := unitDefinitionFiles(t, view); got != 269 {
		t.Fatalf("unit definition files = %d, want the inventory's 269", got)
	}
	catalog, err := content.Compile(view)
	if err != nil {
		t.Fatalf("compile TA Zero: %v", err)
	}
	if len(catalog.Units) != 269 {
		t.Fatalf("compiled units = %d, want the inventory's 269", len(catalog.Units))
	}
	for key, unit := range catalog.Units {
		if unit == nil {
			continue
		}
		if !strings.HasPrefix(unit.Provenance.LogicalPath, "units/") {
			t.Fatalf("unit %s provenance %q kept the content set's own directory name", key, unit.Provenance.LogicalPath)
		}
	}
}

// TestProTAContentSetCompilesUnderItsProfile is the end-to-end proof on real
// content: ProTA renames five trees and ships a ninety-table LOS file, and
// under its config's directory table and read caps the whole catalog compiles
// at the definition count the inventory recorded.
func TestProTAContentSetCompilesUnderItsProfile(t *testing.T) {
	fs := mountWithMod(t, "prota")
	profile := modContent(t, fs, "prota-4.8")
	view := profile.Layout().Apply(fs)
	if got := unitDefinitionFiles(t, view); got != 317 {
		t.Fatalf("unit definition files = %d, want the inventory's 317", got)
	}
	catalog, err := content.CompileWithOptions(view, content.Options{Limits: content.LimitsFromProfile(profile.Limits)})
	if err != nil {
		t.Fatalf("compile ProTA: %v", err)
	}
	if len(catalog.Units) != 317 {
		t.Fatalf("compiled units = %d, want the inventory's 317", len(catalog.Units))
	}
	assertLargeLOSTableCompiled(t, catalog)
	// Isolate the old stock-sized read budget: the baseline now admits this
	// file too, while a caller that explicitly keeps 1 MiB must still refuse it.
	stockLOSCap := content.LimitsFromProfile(profile.Limits)
	stockLOSCap.LOSBytes = 1 << 20
	if _, stockErr := content.CompileLOSTables(view, stockLOSCap); stockErr == nil {
		t.Fatal("a stock-sized battle-table read cap admitted ProTA's enlarged LOS table")
	} else {
		for _, want := range []string{"logical path gamedata/los.tdf", "exceeds read limit"} {
			if !strings.Contains(stockErr.Error(), want) {
				t.Fatalf("stock-sized LOS compile error %q is not the read-cap refusal: it lacks %q", stockErr, want)
			}
		}
	}
}

// TestEscalationReadCapsAdmitItsMapAndLOSTable is the E3 statement for TA:
// Escalation, whose 19.3 MB `[esc] dark prime.tnt` and 2.4 MB `los.tdf` are
// both larger than the former stock-sized read budgets. Under the profile's
// caps both are read; lowering only the relevant byte cap refuses that file
// without changing the profile's definition domain or other limits.
//
// The whole-catalog compile is a separate statement below: unused feature
// definitions do not require their model until a caller requests the feature.
func TestEscalationReadCapsAdmitItsMapAndLOSTable(t *testing.T) {
	fs := mountWithMod(t, "escalation")
	profile := modContent(t, fs, "escalation-10.2.0")
	view := profile.Layout().Apply(fs)
	limits := content.LimitsFromProfile(profile.Limits)

	// Layout must retain the real mount's header-range fast path. That path
	// never reads the whole terrain and is independent of a whole-file cap.
	if _, ok := view.(vfs.RangeReader); !ok {
		t.Fatal("profile layout hid the mount's byte-range capability")
	}
	if _, err := content.CompileMaps(view); err != nil {
		t.Fatalf("ranged map census: %v", err)
	}
	// Deliberately expose only FSOps to exercise the capped fallback, rather
	// than depending on a wrapper accidentally hiding RangeReader.
	wholeFileView := struct{ vfs.FSOps }{view}
	// Keep the profile's definition domain so its unit catalog reaches the
	// map reader; lower only the byte budget whose refusal is being checked.
	const stockTNTBytes, stockLOSBytes = 16 << 20, 1 << 20
	mapCapHeldBack := limits
	mapCapHeldBack.TNTBytes = stockTNTBytes
	if _, err := content.CompileWithOptions(wholeFileView, content.Options{Limits: mapCapHeldBack}); err == nil {
		t.Fatal("a stock-sized map read cap admitted Escalation's oversize terrain file")
	} else {
		for _, want := range []string{"[esc] dark prime.tnt", "exceeds read limit"} {
			if !strings.Contains(strings.ToLower(err.Error()), want) {
				t.Fatalf("stock-sized map compile error %q is not the read-cap refusal: it lacks %q", err, want)
			}
		}
	}
	if _, err := content.CompileWithOptions(wholeFileView, content.Options{Limits: limits}); err != nil {
		t.Fatalf("profile cap should admit the whole-file map census: %v", err)
	}

	stockLOSCap := limits
	stockLOSCap.LOSBytes = stockLOSBytes
	if _, err := content.CompileLOSTables(view, stockLOSCap); err == nil {
		t.Fatal("a stock-sized battle-table read cap admitted Escalation's enlarged LOS table")
	} else {
		for _, want := range []string{"logical path gamedata/los.tdf", "exceeds read limit"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("stock-sized LOS compile error %q is not the read-cap refusal: it lacks %q", err, want)
			}
		}
	}
	tables, err := content.CompileLOSTables(view, limits)
	if err != nil {
		t.Fatalf("LOS tables under the profile's read cap: %v", err)
	}
	assertLargeLOSTable(t, tables)
}

// The Gold 10.2 archive set retains unused corpse definitions whose models it
// does not ship. They are discovery data, not requested feature records
// [05 R-FEAT-01 §1]. Compilation must admit the authored unit set while a later
// request for one of those definitions still fails [02 R-MAP-01 §8].
func TestEscalationContentSetCompilesWithoutUnusedFeatureModels(t *testing.T) {
	fs := mountWithMod(t, "escalation")
	profile := modContent(t, fs, "escalation-10.2.0")
	view := profile.Layout().Apply(fs)
	catalog, err := content.CompileWithOptions(view, content.Options{Limits: content.LimitsFromProfile(profile.Limits)})
	if err != nil {
		t.Fatalf("compile Escalation: %v", err)
	}
	if catalog.Units["armcom"] == nil || catalog.Units["corcom"] == nil {
		t.Fatal("Escalation commanders did not compile")
	}
	assertLargeLOSTableCompiled(t, catalog)
	for _, name := range []string{"armast_dead", "armmanta_dead", "corast_dead", "corcapsub_dead", "cortrog_dead"} {
		if catalog.Features[name] == nil {
			t.Fatalf("unused feature %q was discarded", name)
		}
		err := catalog.ValidateFeatureModels(view, []string{name})
		if err == nil || !strings.Contains(err.Error(), "objects3d/"+name+".3do") || !strings.Contains(err.Error(), "expected valid 3DO model") {
			t.Fatalf("requesting unused feature %q must reject its absent model: %v", name, err)
		}
	}
	// The retail limits still refuse the same content on the definition domain,
	// the first gate it fails.
	if _, retailErr := content.Compile(view); retailErr == nil || !strings.Contains(retailErr.Error(), "549 unit definitions exceed 511 usable IDs in 512-bit domain") {
		t.Fatalf("retail limits no longer refuse 549 definitions: %v", retailErr)
	}
}

// assertLargeLOSTableCompiled is assertLargeLOSTable for a whole catalog.
func assertLargeLOSTableCompiled(t *testing.T, catalog *content.Catalog) {
	t.Helper()
	assertLargeLOSTable(t, catalog.LOS)
}

// assertLargeLOSTable states what E3 bought for the terrain-ray path: the
// compiled table list has one slot per declared table, filled by the loader's
// own naming rule, at a count far past the nine a retail install declares
// [03 R-COMP-02 §1]. Nothing in the compiled shape is bounded by a slot count
// — the retail loader sizes its table list to the declared numtables — so an
// enlarged file is read the way the retail one is.
func assertLargeLOSTable(t *testing.T, tables *content.LOSTables) {
	t.Helper()
	if tables == nil {
		t.Fatal("no LOS tables compiled")
	}
	if tables.NumTables <= 9 {
		t.Fatalf("numtables = %d, want the content set's enlarged table", tables.NumTables)
	}
	if len(tables.Tables) < int(tables.NumTables) {
		t.Fatalf("compiled %d tables for %d declared slots", len(tables.Tables), tables.NumTables)
	}
	for slot := 0; slot < int(tables.NumTables); slot++ {
		if got := tables.Tables[slot].TableNum; got != slot+1 {
			t.Fatalf("slot %d holds TABLE%d, want TABLE%d", slot, got, slot+1)
		}
	}
	// The highest table any observer reaches is the one below the declared
	// count, and it must carry spokes: an empty top table would mean the
	// enlarged file had been read but not filled.
	top := tables.Tables[tables.NumTables-2]
	if len(top.Lines) == 0 {
		t.Fatalf("TABLE%d, the highest reachable table, compiled no lines", top.TableNum)
	}
}

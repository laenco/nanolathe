package content

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/vfs"
)

// Faction declarations compose beside the base tables and can never replace
// a base definition (docs/DESIGN_MODS_MUTATORS.md §5.8). The fixtures mount
// real archives so directory discovery runs through the overlay.

func sideSection(ordinal int, name, commander string) string {
	var b strings.Builder
	b.WriteString("[SIDE" + itoaTest(ordinal) + "]{name=" + name + ";nameprefix=" + name[:3] + ";commander=" + commander + ";font=console;fontgui=armbutt;")
	for _, anchor := range sideAnchorNames {
		b.WriteString("[" + anchor + "]{x1=1;y1=2;x2=3;y2=4;}")
	}
	b.WriteString("}\n")
	return b.String()
}

func itoaTest(n int) string { return string(rune('0' + n)) }

func mountFixtureArchives(t *testing.T, archives ...map[string]string) *vfs.FS {
	t.Helper()
	// Cleanups run last first: the directory is made before Close is
	// registered so the archives are closed before it is removed, which
	// Windows refuses while a file in it is open.
	dir := t.TempDir()
	fs := vfs.New()
	t.Cleanup(func() { fs.Close() })
	for i, files := range archives {
		var list []vfs.ArchiveFile
		for p, data := range files {
			list = append(list, vfs.ArchiveFile{Path: p, Data: []byte(data)})
		}
		name := filepath.Join(dir, "fixture"+itoaTest(i)+".ufo")
		f, err := os.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := vfs.WriteArchive(f, list, vfs.ArchiveWriteOptions{}); err != nil {
			t.Fatal(err)
		}
		f.Close()
		if _, err := fs.MountArchive(name, i); err != nil {
			t.Fatal(err)
		}
	}
	return fs
}

func baseFixture() map[string]string {
	return map[string]string{
		"gamedata/sidedata.tdf": sideSection(0, "ARM", "ARMCOM") + sideSection(1, "CORE", "CORCOM") +
			"[CANBUILD]{[ARMCOM]{canbuild1=ARMLAB;}}",
		"gamedata/moveinfo.tdf": "[CLASS0]{Name=TANKSH2;FootprintX=2;FootprintZ=2;MaxSlope=20;}",
		"gamedata/sound.tdf":    "[ARM_TANK]{select1=tank;}[CORE_TANK]{select1=tank2;}",
		"weapons/base.tdf":      "[ARM_LASER]{ID=19;range=300;}[CORE_GUN]{ID=255;range=400;}",
	}
}

func factionFixture(id, side, commander string, weaponIDs ...string) map[string]string {
	dir := "factions/" + id + "/"
	weapons := ""
	for i, authored := range weaponIDs {
		weapons += "[" + id + "_W" + itoaTest(i) + "]{ID=" + authored + ";range=500;}"
	}
	return map[string]string{
		dir + "sidedata.tdf": sideSection(0, side, commander) + "[CANBUILD]{[" + commander + "]{canbuild1=" + id + "LAB;}}",
		dir + "moveinfo.tdf": "[CLASS0]{Name=" + id + "_TANK;FootprintX=3;FootprintZ=3;}",
		dir + "sound.tdf":    "[" + id + "_TANK]{select1=" + id + "sel;}",
		dir + "weapons.tdf":  weapons,
	}
}

func TestFactionDeclarationsComposeAfterTheBase(t *testing.T) {
	// Both factions author ID 19, which a base weapon owns, and one ID 255.
	fs := mountFixtureArchives(t, baseFixture(),
		factionFixture("myn", "Mynn", "RACCOM", "19", "255"),
		factionFixture("rhy", "RHYOSS", "RHYCOM", "19"))
	sides, err := CompileSides(fs)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for i, s := range sides {
		if s.Index != i {
			t.Fatalf("side %s has ordinal %d at position %d", s.Name, s.Index, i)
		}
		names = append(names, s.Name)
	}
	// Base sides keep 0 and 1; declarations follow in folded id order.
	if got := strings.Join(names, ","); got != "ARM,CORE,Mynn,RHYOSS" {
		t.Fatalf("sides %s", got)
	}
	menus, err := CompileBuildMenus(fs)
	if err != nil || menus["armcom"] == nil || menus["raccom"] == nil || menus["rhycom"] == nil {
		t.Fatalf("build menus %v err %v", menus, err)
	}
	moves, err := CompileMovement(fs)
	if err != nil || moves["tanksh2"] == nil || moves["myn_tank"] == nil || moves["rhy_tank"] == nil {
		t.Fatalf("movement %v err %v", moves, err)
	}
	cats, order, err := compileSoundCategoriesOrdered(fs)
	if err != nil || len(cats) != 4 || order[0].Name != "ARM_TANK" || order[1].Name != "CORE_TANK" {
		t.Fatalf("sound categories: %d, err %v; base ordinals must not move", len(cats), err)
	}
	result, err := compileWeapons(fs, RetailLimits())
	if err != nil {
		t.Fatal(err)
	}
	w := result.weapons
	// The base records keep their authored slots; faction weapons take the
	// slots past the retail table in declaration order, whatever they author.
	want := map[string]int32{"arm_laser": 19, "core_gun": 255, "myn_w0": 257, "myn_w1": 258, "rhy_w0": 259}
	for name, id := range want {
		if w[name] == nil || w[name].ID != id {
			t.Fatalf("weapon %s: %+v, want slot %d", name, w[name], id)
		}
	}
	limits, err := factionLimits(fs, RetailLimits(), w, 600)
	if err != nil {
		t.Fatal(err)
	}
	if limits.Weapons != 260 || limits.Units != 608 {
		t.Fatalf("widened limits %+v, want 260 weapon slots and a 608-ID domain", limits)
	}
	again, err := factionLimits(fs, limits, w, 600)
	if err != nil || again != limits {
		t.Fatalf("widening is not idempotent: %+v then %+v", limits, again)
	}
}

func TestFactionSlotsSkipAZeroActiveByte(t *testing.T) {
	// Slot 256 would truncate to an active byte of zero, the inactive gate.
	if got := nextFactionWeaponSlot(256); got != 257 {
		t.Fatalf("slot 256 -> %d, want 257", got)
	}
	if got := nextFactionWeaponSlot(512); got != 513 {
		t.Fatalf("slot 512 -> %d, want 513", got)
	}
	if got := nextFactionWeaponSlot(300); got != 300 {
		t.Fatalf("slot 300 -> %d", got)
	}
}

func TestFactionCannotRedefineABaseName(t *testing.T) {
	cases := map[string]map[string]string{
		"side":           {"factions/x/sidedata.tdf": sideSection(0, "CORE", "XCOM")},
		"movement class": {"factions/x/sidedata.tdf": sideSection(0, "XXX", "XCOM"), "factions/x/moveinfo.tdf": "[CLASS0]{Name=TANKSH2;}"},
		"sound category": {"factions/x/sidedata.tdf": sideSection(0, "XXX", "XCOM"), "factions/x/sound.tdf": "[ARM_TANK]{select1=x;}"},
		"weapon":         {"factions/x/sidedata.tdf": sideSection(0, "XXX", "XCOM"), "factions/x/weapons.tdf": "[ARM_LASER]{ID=3;}"},
		"build menu":     {"factions/x/sidedata.tdf": sideSection(0, "XXX", "XCOM") + "[CANBUILD]{[ARMCOM]{canbuild1=XLAB;}}"},
	}
	for kind, faction := range cases {
		fs := mountFixtureArchives(t, baseFixture(), faction)
		var err error
		switch kind {
		case "side":
			_, err = CompileSides(fs)
		case "movement class":
			_, err = CompileMovement(fs)
		case "sound category":
			_, _, err = compileSoundCategoriesOrdered(fs)
		case "weapon":
			_, err = compileWeapons(fs, RetailLimits())
		case "build menu":
			_, err = CompileBuildMenus(fs)
		}
		if err == nil || !strings.Contains(err.Error(), "faction defines a "+kind) {
			t.Fatalf("%s: err %v, want the faction-duplicate refusal", kind, err)
		}
	}
}

func TestNoDeclarationKeepsTheBaseCompile(t *testing.T) {
	fs := mountFixtureArchives(t, baseFixture())
	if factions, err := DiscoverFactions(fs); err != nil || len(factions) != 0 {
		t.Fatalf("factions %v err %v", factions, err)
	}
	result, err := compileWeapons(fs, RetailLimits())
	if err != nil {
		t.Fatal(err)
	}
	limits, err := factionLimits(fs, RetailLimits(), result.weapons, 600)
	if err != nil || limits != RetailLimits() {
		t.Fatalf("limits changed without a declaration: %+v err %v", limits, err)
	}
}

// fixtureUnits declares the units the mounted archives supply, each with the
// provenance of its own units/<name>.fbi, as the unit compiler records it.
func fixtureUnits(t *testing.T, fs vfs.FSOps, names ...string) map[string]*UnitDef {
	t.Helper()
	units := map[string]*UnitDef{}
	for _, name := range names {
		info, err := fs.Stat("units/" + strings.ToLower(name) + ".fbi")
		if err != nil {
			t.Fatal(err)
		}
		key := CanonicalKey(name)
		units[key] = &UnitDef{DefinitionHeader: DefinitionHeader{CanonicalKey: key, Provenance: ProvenanceFrom(info)}, UnitName: name}
	}
	return units
}

// A declaration's damage.tdf adds base weapons' damage against its own units
// and nothing else: entries from different add-ons compose, the weapon keeps
// its authored hash and entries, and an entry naming a base unit, another
// add-on's unit or a faction weapon is refused. One for a weapon the content
// lacks, or a unit the table already names, places nothing and keeps the
// table's own entry, as under a mod (docs/DESIGN_MODS_MUTATORS.md §5.8).
func TestFactionDamageNamesOnlyItsOwnUnits(t *testing.T) {
	base := baseFixture()
	base["weapons/base.tdf"] = "[ARM_LASER]{ID=19;range=300;[DAMAGE]{default=10;ARMCOM=5;}}[CORE_GUN]{ID=255;range=400;[DAMAGE]{default=8;RHYTANK=1;}}"
	base["units/armcom.fbi"] = "[UNITINFO]{UnitName=ARMCOM;side=ARM;}"
	rhy := factionFixture("rhy", "RHYOSS", "RHYCOM", "3")
	rhy["units/rhytank.fbi"] = "[UNITINFO]{UnitName=RHYTANK;side=RHYOSS;}"
	tll := factionFixture("tll", "TLL", "TLLCOM")
	tll["units/tlltank.fbi"] = "[UNITINFO]{UnitName=TLLTANK;side=TLL;}"
	compose := func(rhyDamage, tllDamage string) (map[string]*WeaponDef, []FactionDamage, error) {
		rhy["factions/rhy/damage.tdf"] = rhyDamage
		tll["factions/tll/damage.tdf"] = tllDamage
		fs := mountFixtureArchives(t, base, rhy, tll)
		result, err := compileWeapons(fs, RetailLimits())
		if err != nil {
			t.Fatal(err)
		}
		entries, err := composeFactionDamage(fs, result.weapons, fixtureUnits(t, fs, "ARMCOM", "RHYTANK", "TLLTANK"))
		return result.weapons, entries, err
	}
	weapons, entries, err := compose("[ARM_LASER]{rhytank=40;}", "[arm_laser]{TLLTANK=7;}")
	if err != nil {
		t.Fatal(err)
	}
	laser := weapons["arm_laser"]
	for unit, want := range map[string]int32{"ARMCOM": 5, "RHYTANK": 40, "tlltank": 7} {
		if got, ok := laser.DamageOverride(unit); !ok || got != want {
			t.Fatalf("ARM_LASER vs %s = %d (%v), want %d", unit, got, ok, want)
		}
	}
	if laser.DamageDefault != 10 || len(entries) != 2 || entries[0].Unit != "rhytank" || entries[1].Unit != "TLLTANK" {
		t.Fatalf("default %d, entries %+v", laser.DamageDefault, entries)
	}
	alone, _, err := compose("", "")
	if err != nil || alone["arm_laser"].Hash != laser.Hash {
		t.Fatalf("the composed weapon's hash moved from its authored record's (err %v)", err)
	}
	for _, c := range []struct{ rhy, want string }{
		{"[ARM_LASER]{ARMCOM=40;}", "does not define"},
		{"[ARM_LASER]{TLLTANK=40;}", "does not define"},
		{"[RHY_W0]{RHYTANK=40;}", "names no base weapon"},
	} {
		if _, _, err := compose(c.rhy, ""); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("damage.tdf %s: err %v, want %q", c.rhy, err, c.want)
		}
	}
	weapons, entries, err = compose("[CORE_GUN]{RHYTANK=40;}[MOD_REMOVED]{RHYTANK=9;}", "")
	if got, _ := weapons["core_gun"].DamageOverride("RHYTANK"); err != nil || len(entries) != 0 || got != 1 {
		t.Fatalf("entries the content cannot take: err %v, entries %+v, CORE_GUN vs RHYTANK %d", err, entries, got)
	}
}

// A declaration's menu entries fill new pages after every page a builder
// already has, and the next declaration starts on the page after, so two
// add-ons never claim one button (docs/DESIGN_MODS_MUTATORS.md §5.8).
func TestFactionMenuEntriesTakeTheirOwnPages(t *testing.T) {
	packA := map[string]string{"factions/a/sidedata.tdf": "[CANBUILD]{}"}
	var entries strings.Builder
	var names []string
	for i := 1; i <= 7; i++ {
		name := "AUNIT" + itoaTest(i)
		names = append(names, name)
		packA["units/"+strings.ToLower(name)+".fbi"] = "[UNITINFO]{UnitName=" + name + ";side=ARM;}"
		entries.WriteString("[E" + itoaTest(i) + "]{UNITMENU=ARMLAB;UNITNAME=" + name + ";}")
	}
	// An entry for a builder this content lacks places nothing.
	entries.WriteString("[E8]{UNITMENU=NOSUCHLAB;UNITNAME=AUNIT1;}")
	packA["factions/a/download.tdf"] = entries.String()
	packB := map[string]string{
		"factions/b/sidedata.tdf": "[CANBUILD]{}",
		"factions/b/download.tdf": "[E1]{UNITMENU=armlab;UNITNAME=BUNIT;}",
		"units/bunit.fbi":         "[UNITINFO]{UnitName=BUNIT;side=ARM;}",
	}
	base := baseFixture()
	base["units/armlab.fbi"] = "[UNITINFO]{UnitName=ARMLAB;side=ARM;}"
	fs := mountFixtureArchives(t, base, packA, packB)
	units := fixtureUnits(t, fs, append(names, "ARMLAB", "BUNIT")...)
	units["armlab"].Builder = true
	units["armlab"].BuildPageCount = 3 // armlab1.gui and armlab2.gui
	retail := []DownloadMenuPlacement{{Builder: "ARMLAB", Menu: 4, Button: 2, Product: "ARMCOM", BuilderResolved: true, ProductResolved: true}}
	placements, err := composeFactionPlacements(fs, units, retail)
	if err != nil {
		t.Fatal(err)
	}
	type slot struct {
		menu, button uint8
		product      string
	}
	want := []slot{{4, 2, "ARMCOM"}, {5, 0, "AUNIT1"}, {5, 1, "AUNIT2"}, {5, 2, "AUNIT3"}, {5, 3, "AUNIT4"}, {5, 4, "AUNIT5"}, {5, 5, "AUNIT6"}, {6, 0, "AUNIT7"}, {7, 0, "BUNIT"}}
	if len(placements) != len(want) {
		t.Fatalf("placements %+v", placements)
	}
	for i, p := range placements {
		if (slot{p.Menu, p.Button, p.Product}) != want[i] || !p.BuilderResolved || !p.ProductResolved {
			t.Fatalf("placement %d = %+v, want %+v", i, p, want[i])
		}
	}
	packB["factions/b/download.tdf"] = "[E1]{UNITMENU=ARMLAB;UNITNAME=AUNIT1;}"
	fs = mountFixtureArchives(t, base, packA, packB)
	units = fixtureUnits(t, fs, append(names, "ARMLAB", "BUNIT")...)
	units["armlab"].Builder = true
	if _, err := composeFactionPlacements(fs, units, nil); err == nil || !strings.Contains(err.Error(), "does not define") {
		t.Fatalf("an entry for another add-on's unit: err %v", err)
	}
}

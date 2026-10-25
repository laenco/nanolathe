package main

import (
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/gui"
	"github.com/nanolathe-gg/nanolathe/internal/input"
	"github.com/nanolathe-gg/nanolathe/internal/maplibrary"
	"github.com/nanolathe-gg/nanolathe/internal/modfetch"
	"github.com/nanolathe-gg/nanolathe/internal/modlibrary"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/ui"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

func TestMapRequestResolvesOnlyRequiredDependenciesOnce(t *testing.T) {
	a := modfetch.Entry{ID: "trees"}
	b := modfetch.Entry{ID: "rocks"}
	entry := modfetch.Entry{ID: "islands", Map: "maps/Islands.ota", Requires: []string{"rocks", "trees", "rocks"}}
	got, err := mapDownloadEntries(entry, []modfetch.Entry{a, b, {ID: "unused"}})
	if err != nil || !reflect.DeepEqual(got, []modfetch.Entry{b, a, entry}) {
		t.Fatalf("request entries = %+v, %v", got, err)
	}
	entry.Requires = append(entry.Requires, "missing")
	if _, err := mapDownloadEntries(entry, []modfetch.Entry{a, b}); err == nil {
		t.Fatal("missing feature pack accepted")
	}
}

func TestMapLibraryMountPreservesBasePrecedenceAndCaptureIsolation(t *testing.T) {
	base, _ := modFixture(t)
	writeModFixtureFile(t, base, "maps/Existing.ota", "base")
	root, err := maplibrary.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	lib, err := maplibrary.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	writeModFixtureFile(t, src, modlibrary.MetadataFile, `{"schema":1,"id":"fixture-map","name":"Fixture map","version":"1"}`)
	writeModFixtureFile(t, src, "maps/Existing.ota", "downloaded")
	writeModFixtureFile(t, src, "maps/Existing.tnt", "paired fixture")
	writeModFixtureFile(t, src, "maps/New.tnt", "paired fixture")
	writeModFixtureFile(t, src, "maps/New.ota", `[GlobalHeader]{SCHEMACOUNT=1;[Schema 0]{Type=Network 1;}}`)
	installed, err := lib.InstallDirectory(src, modlibrary.InstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mountContent(Options{}, []string{base}, modSelection{})
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if cs.limits.TNTBytes < maplibrary.MaxTNTBytes {
		t.Fatal("downloaded terrain read cap missing")
	}
	got, err := cs.fs.ReadFileLimit("maps/Existing.ota", 1024)
	if err != nil || string(got) != "base" {
		t.Fatalf("base precedence = %q, %v", got, err)
	}
	if cs.root != base || !reflect.DeepEqual(cs.baseRoots, []string{base}) || !reflect.DeepEqual(cs.roots, []string{installed.Dir, base}) {
		t.Fatalf("root ownership: root=%s base=%v all=%v", cs.root, cs.baseRoots, cs.roots)
	}
	for _, opts := range []Options{{Headless: true}, {Shot: "capture"}, {NLShot: "capture"}, {BattleBenchmark: "capture"}} {
		roots, skipped, err := installedMapRoots(opts)
		if err != nil || len(roots) != 0 || len(skipped) != 0 {
			t.Fatalf("capture mounts saved map library: %v, %v, %v", roots, skipped, err)
		}
	}
	// Deliberate explicit roots still work in a capture.
	explicit, err := mountContent(Options{Headless: true}, []string{installed.Dir, base}, modSelection{manual: true})
	if err != nil {
		t.Fatal(err)
	}
	defer explicit.Close()
	if _, err := explicit.fs.Stat("maps/New.ota"); err != nil {
		t.Fatal(err)
	}
}

// Downloaded maps never stop a start. File-manager clutter is ignored; a
// package that fails its mount-time audit, or collides with a base feature,
// is left out with a notice naming its directory while the others mount, and
// a left-out map stays removable.
func TestMapLibraryMountSkipsBadPackagesAndStillStarts(t *testing.T) {
	base, _ := modFixture(t)
	writeModFixtureFile(t, base, "features/z/base.tdf", "[tree]{metal=10;}")
	root, err := maplibrary.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	lib, err := maplibrary.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	install := func(id string, files map[string]string) modlibrary.Mod {
		t.Helper()
		src := t.TempDir()
		writeModFixtureFile(t, src, modlibrary.MetadataFile, `{"schema":1,"id":"`+id+`","name":"`+id+`","version":"1"}`)
		writeModFixtureFile(t, src, "maps/"+id+".ota", `[GlobalHeader]{SCHEMACOUNT=1;[Schema 0]{Type=Network 1;}}`)
		writeModFixtureFile(t, src, "maps/"+id+".tnt", "paired fixture")
		for name, body := range files {
			writeModFixtureFile(t, src, name, body)
		}
		m, err := lib.InstallDirectory(src, modlibrary.InstallOptions{})
		if err != nil {
			t.Fatal(err)
		}
		// Removal is offered only for downloaded packages.
		writeModFixtureFile(t, m.Dir, modlibrary.ReceiptFile, `{"sha256":"`+strings.Repeat("a", 64)+`","size":1,"source":"https://nanolathe.gg/maps/`+id+`.zip","installed":"2026-10-05T00:00:00Z"}`)
		return m
	}
	good := install("good", nil)
	bad := install("bad", nil)
	collides := install("collides", map[string]string{"features/a/override.tdf": "[tree]{metal=20;}"})
	for _, name := range []string{".DS_Store", "maps/.DS_Store", "Thumbs.db", "desktop.ini", "._x", "maps/._good.ota", "__MACOSX/maps/._good.tnt"} {
		writeModFixtureFile(t, good.Dir, name, "\x00\x05\x16\x07")
	}
	writeModFixtureFile(t, bad.Dir, "units/after-install.fbi", "not map content")

	cs, err := mountContent(Options{}, []string{base}, modSelection{})
	if err != nil {
		t.Fatalf("a damaged map package stopped the mount: %v", err)
	}
	g := &gameShell{cs: cs, frontend: ui.NewFrontend(modeMenuMap)}
	defer func() { g.releaseAudio(); g.cs.Close() }()
	if !reflect.DeepEqual(cs.roots, []string{good.Dir, base}) {
		t.Fatalf("mounted roots = %v", cs.roots)
	}
	if _, err := cs.fs.Stat("maps/good.ota"); err != nil {
		t.Fatal(err)
	}
	var skipped []string
	for _, s := range cs.skippedMaps {
		skipped = append(skipped, s.Dir)
	}
	if !reflect.DeepEqual(skipped, []string{bad.Dir, collides.Dir}) {
		t.Fatalf("skipped = %v", skipped)
	}
	for _, want := range []struct{ dir, reason string }{{bad.Dir, "non-map content"}, {collides.Dir, "changes an existing feature"}} {
		if !slices.ContainsFunc(cs.notes, func(note string) bool {
			return strings.Contains(note, want.dir) && strings.Contains(note, want.reason)
		}) {
			t.Fatalf("no notice names %s (%s): %q", want.dir, want.reason, cs.notes)
		}
	}

	installed, err := lib.Installed()
	if err != nil {
		t.Fatal(err)
	}
	for logical, want := range map[string]string{"maps/good.ota": good.Dir, "maps/bad.ota": bad.Dir, "maps/collides.ota": collides.Dir} {
		if got := g.removableMapProvider(logical, installed); got == nil || got.Dir != want {
			t.Fatalf("%s removal target = %+v, want %s", logical, got, want)
		}
	}
	_, target := g.removableLibraryMap("maps/bad.ota")
	if target == nil {
		t.Fatal("skipped package is not removable")
	}
	if err := g.removeLibraryMap("maps/bad.ota", *target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bad.Dir); !os.IsNotExist(err) {
		t.Fatalf("skipped package survived removal: %v", err)
	}
	if g.cs.mapSkipped(bad.Dir) != nil || g.cs.mapSkipped(collides.Dir) == nil || !slices.Contains(g.cs.roots, good.Dir) {
		t.Fatalf("remount after removal: roots %v skipped %+v", g.cs.roots, g.cs.skippedMaps)
	}
}

func TestMapLibraryRefreshPreservesLiveSetupAndManualConfig(t *testing.T) {
	base, _ := modFixture(t)
	config := t.TempDir() + "/config.json"
	writeModFixtureFile(t, strings.TrimSuffix(config, "/config.json"), "config.json", `{"schema":2,"id":"fixture-config","name":"Fixture config","version":"1","content":{}}`)
	opts := Options{ModConfig: config}
	cs, err := mountContent(opts, []string{base}, modSelection{manual: true})
	if err != nil {
		t.Fatal(err)
	}
	g := &gameShell{opts: opts, cs: cs, frontend: ui.NewFrontend(modeMenuMap), setup: newSkirmishMenuConfig("kept"), mapReturn: modeMenuSkirmish, survivalMenu: true}
	g.setup.UnitLimit = 321
	g.setup.Players[1].Nickname = "Kept player"
	before := g.setup
	defer func() { g.releaseAudio(); g.cs.Close() }()
	if err := g.refreshMapLibrary(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g.setup, before) || !g.survivalMenu || g.mapReturn != modeMenuSkirmish || g.frontend.Mode != modeMenuMap {
		t.Fatal("remount replaced live match setup")
	}
	if g.cs.configPath != config || !g.cs.manualRoots || g.cs.root != base {
		t.Fatalf("remount lost config/base identity: %+v", g.cs)
	}
}

func TestGetMapsButtonKeepsOriginalNavigation(t *testing.T) {
	w := &gui.Window{Gadgets: []gui.Gadget{{Name: "HEADER"}, {Name: "LOAD", Text: "Load", Rect: gui.Rect{X: 200, Y: 300, W: 120, H: 24}}, {Name: "PREVMENU", Text: "Back"}, {Name: "MAPPIC", Rect: gui.Rect{X: 200, Y: 100, W: 120, H: 120}}}}
	original := append([]gui.Gadget(nil), w.Gadgets...)
	installGetMapsButton(w)
	if !reflect.DeepEqual(w.Gadgets[:len(original)], original) || w.GadgetIndex("GETMAPS") < 0 {
		t.Fatal("map catalogue changed original navigation")
	}
	button := w.Gadgets[w.GadgetIndex("GETMAPS")]
	preview := w.Gadgets[w.GadgetIndex("MAPPIC")]
	if button.Rect.Y+button.Rect.H >= preview.Rect.Y {
		t.Fatal("More maps is not above the minimap")
	}
	if button.Text != "More maps" {
		t.Fatal("catalogue label no longer fits compact button")
	}
}

func TestMapLibraryHonorsProfileDirectoryRedirects(t *testing.T) {
	if !mapLibraryLayoutSupported(vfs.NewLayout(map[string]string{"units": "unitsP"})) {
		t.Fatal("unrelated profile path disables maps")
	}
	for _, family := range []string{"maps", "features", "anims", "objects3d", "textures"} {
		if mapLibraryLayoutSupported(vfs.NewLayout(map[string]string{family: family + "P"})) {
			t.Fatalf("renamed %s directory admitted standard map packages", family)
		}
	}
}

func TestMapPreviewCompletionBelongsToCurrentSelection(t *testing.T) {
	picture := image.NewRGBA(image.Rect(0, 0, 2, 2))
	state := &mapsFetch{previewSerial: 2, previewStatus: "Loading preview..."}
	state.finishPreview(1, picture, nil)
	if state.preview != nil || state.previewStatus != "Loading preview..." {
		t.Fatal("old selection replaced current preview")
	}
	state.finishPreview(2, picture, nil)
	if state.preview != picture || state.previewStatus != "" {
		t.Fatal("current selection did not publish")
	}
	state.previewSerial++
	state.finishPreview(3, nil, errors.New("offline"))
	if state.preview != nil || state.previewStatus != "Preview unavailable" {
		t.Fatal("failed preview kept an unrelated image")
	}
	state.stopPreview()
	state.finishPreview(3, picture, nil)
	if state.preview != nil {
		t.Fatal("closed chooser accepted a late preview")
	}
}

func TestMapPreviewUsesActivePaletteAndPreservesAspect(t *testing.T) {
	source := image.NewPaletted(image.Rect(0, 0, 2, 1), color.Palette{color.RGBA{R: 255, A: 255}, color.RGBA{G: 255, A: 255}})
	source.Pix = []byte{0, 1}
	var palette [256][4]byte
	palette[42] = [4]byte{255, 0, 0, 0}
	palette[17] = [4]byte{0, 255, 0, 0}
	got := mapPreviewIndexed(source, palette, 4, 4)
	want := []byte{0, 0, 0, 0, 42, 42, 17, 17, 42, 42, 17, 17, 0, 0, 0, 0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("preview palette/aspect = %v", got)
	}
}

func TestMapRemovalProtectsWinningRetailModAndManualRoots(t *testing.T) {
	base, _ := modFixture(t)
	ota := `[GlobalHeader]{SCHEMACOUNT=1;[Schema 0]{Type=Network 1;}}`
	writeModFixtureFile(t, base, "maps/Retail.ota", ota)
	writeModFixtureFile(t, base, "maps/Retail.tnt", "paired fixture")
	root, _ := maplibrary.DefaultRoot()
	lib, err := maplibrary.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	writeModFixtureFile(t, source, modlibrary.MetadataFile, `{"schema":1,"id":"downloaded","name":"Downloaded","version":"1"}`)
	for _, name := range []string{"Retail", "Mod", "Downloaded"} {
		writeModFixtureFile(t, source, "maps/"+name+".ota", ota)
		writeModFixtureFile(t, source, "maps/"+name+".tnt", "paired fixture")
	}
	installed, err := lib.InstallDirectory(source, modlibrary.InstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The fixture receipt distinguishes a fetched package from a local folder.
	receipt := modlibrary.Receipt{Source: "https://nanolathe.gg/maps/downloaded.zip", SHA256: strings.Repeat("a", 64), Size: 100}
	raw, _ := json.Marshal(receipt)
	writeModFixtureFile(t, installed.Dir, modlibrary.ReceiptFile, string(raw))
	modRoot := t.TempDir()
	writeModFixtureFile(t, modRoot, "maps/Mod.ota", ota)
	writeModFixtureFile(t, modRoot, "maps/Mod.tnt", "paired fixture")
	cs, err := mountContent(Options{}, []string{base}, modSelection{mod: &modlibrary.Mod{Dir: modRoot}})
	if err != nil {
		t.Fatal(err)
	}
	g := &gameShell{cs: cs, frontend: ui.NewFrontend(modeMenuMap), setup: newSkirmishMenuConfig("Downloaded"), maps: []string{"Downloaded"}}
	defer func() { g.releaseAudio(); g.cs.Close() }()
	for _, name := range []string{"Retail", "Mod"} {
		if _, target := g.removableLibraryMap("maps/" + name + ".ota"); target != nil {
			t.Fatalf("%s-owned map exposed delete", name)
		}
	}
	_, target := g.removableLibraryMap("maps/Downloaded.ota")
	if target == nil {
		t.Fatal("downloaded map cannot be removed")
	}
	g.cs.manualRoots = true
	if _, target := g.removableLibraryMap("maps/Downloaded.ota"); target != nil {
		t.Fatal("manual roots expose delete")
	}
	g.cs.manualRoots = false
	g.battle = &battleSession{}
	if err := g.removeLibraryMap("maps/Downloaded.ota", *target); err == nil {
		t.Fatal("battle allowed removal")
	}
	g.battle = nil
	// A start point placed on the selected map survives a failed removal and
	// goes with the map when the removal succeeds.
	g.setup.Players[1].Start = session.StartPoint{Set: true, X: 64, Z: 64}
	before := g.setup
	// A failed atomic removal must restore the mounted map and its selection.
	if err := os.Remove(lib.StagingDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lib.StagingDir(), []byte("block removal"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := g.removeLibraryMap("maps/Downloaded.ota", *target); err == nil {
		t.Fatal("blocked removal succeeded")
	}
	if _, err := g.cs.fs.Stat("maps/Downloaded.ota"); err != nil {
		t.Fatal("failed removal did not restore mount", err)
	}
	if g.maps[g.mapIdx] != "Downloaded" || !reflect.DeepEqual(g.setup, before) {
		t.Fatal("failed removal changed selection/setup")
	}
	if err := os.Remove(lib.StagingDir()); err != nil {
		t.Fatal(err)
	}
	if err := g.removeLibraryMap("maps/Downloaded.ota", *target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(installed.Dir); !os.IsNotExist(err) {
		t.Fatalf("package remains: %v", err)
	}
	if g.setup.MapName == "Downloaded" || g.opts.excludedMapRoot != "" {
		t.Fatal("removed selection or temporary mount exclusion persisted")
	}
	if g.setup.Players[1].Start.Set {
		t.Fatal("the removed map's start point stayed with its replacement")
	}
	before.MapName = g.setup.MapName
	before.Players[1].Start = session.StartPoint{}
	if !reflect.DeepEqual(g.setup, before) {
		t.Fatal("removal changed other setup fields")
	}
	if _, err := g.cs.fs.Stat("maps/Retail.ota"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.cs.fs.Stat("maps/Mod.ota"); err != nil {
		t.Fatal(err)
	}
}

// These host row controls share one clipped rectangle for drawing and input;
// they must not inherit the list's selection or activation gesture.
func TestMapRemoveRowsClipAndCapture(t *testing.T) {
	p := ui.NewPanel(&gui.Window{Gadgets: []gui.Gadget{{Kind: gui.KindPanel}, {Name: "MAPNAMES", Kind: gui.KindListBox, Active: 1, Attribs: 1, ItemHeight: 16, Rect: gui.Rect{X: 20, Y: 30, W: 100, H: 40}}}})
	p.FillTextListAt(1, []string{"retail", "downloaded", "downloaded too", "last"}, nil, 8)
	g := &gameShell{cs: &contentSet{}, frontend: ui.NewFrontend(modeMenuMap)}
	target := &modlibrary.Mod{}
	g.mapRemovals = mapRemovalRows{panel: p, content: g.cs, targets: []*modlibrary.Mod{nil, target, target, target}}
	if _, ok := g.mapRemoveRowRect(p, 1, 0); ok {
		t.Fatal("retail row has an X")
	}
	r, ok := g.mapRemoveRowRect(p, 1, 1)
	if !ok {
		t.Fatal("complete downloaded row lacks X")
	}
	if _, ok := g.mapRemoveRowRect(p, 1, 2); ok {
		t.Fatal("bottom partial row exposes clipped X")
	}
	in := &input.State{Mouse: &input.MouseState{}, Kbd: &input.KeyboardState{}}
	for x := r.X; x < r.X+r.W; x++ {
		in.Mouse.ResetEdges()
		in.Mouse.SetPosition(float32(x), float32(r.Y+r.H/2))
		in.Mouse.SetButton(input.MouseButtonLeft, true)
		g.serviceMenuWidgets(p, in)
		if g.mapRemovals.pressed != 2 {
			t.Fatalf("X pixel %d did not receive pointer", x)
		}
		in.Mouse.ResetEdges()
		in.Mouse.SetPosition(0, 0)
		in.Mouse.SetButton(input.MouseButtonLeft, false)
		g.serviceMenuWidgets(p, in)
		if g.mapRemovals.pressed != 0 {
			t.Fatal("outside release retained capture")
		}
	}
	in.Mouse.ResetEdges()
	in.Mouse.SetPosition(float32(r.X+r.W/2), float32(r.Y+r.H/2))
	in.Mouse.SetButton(input.MouseButtonLeft, true)
	g.serviceMenuWidgets(p, in)
	if g.mapRemovals.pressed != 2 {
		t.Fatal("X did not capture pointer")
	}
	in.Mouse.ResetEdges()
	in.Mouse.SetWheel(0, -1)
	g.serviceMenuWidgets(p, in)
	if g.mapRemovals.pressed != -1 {
		t.Fatal("scroll did not cancel captured action")
	}
	in.Mouse.ResetEdges()
	in.Mouse.SetButton(input.MouseButtonLeft, false)
	g.serviceMenuWidgets(p, in)
	if g.mapRemovals.pressed != 0 {
		t.Fatal("cancelled gesture retained capture")
	}
	p.SetListTopAt(1, 1, p.ListMaxTopAt(1))
	if _, ok := g.mapRemoveRowRect(p, 1, 0); ok {
		t.Fatal("scrolled-off row exposes X")
	}
	shifted, ok := g.mapRemoveRowRect(p, 1, 1)
	if !ok || shifted.Y >= r.Y {
		t.Fatal("row X did not follow scroll")
	}
	g.cs.manualRoots = true
	if _, ok := g.mapRemoveRowRect(p, 1, 1); ok {
		t.Fatal("manual mount retained X")
	}
}

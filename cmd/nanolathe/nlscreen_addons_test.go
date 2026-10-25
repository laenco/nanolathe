package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/nanolathe-gg/nanolathe/internal/addonlibrary"
	"github.com/nanolathe-gg/nanolathe/internal/modfetch"
	"github.com/nanolathe-gg/nanolathe/internal/modlibrary"
	"github.com/nanolathe-gg/nanolathe/internal/platform/screenkit"
	"github.com/nanolathe-gg/nanolathe/internal/settings"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

// wait returns once no listing runs.
func (l *nlAddonList) wait() { l.wg.Wait() }

// addonScreenFixture serves a local add-on catalogue offering one add-on — a
// package holding one small authored archive — and returns a screen whose
// base folder lacks it, the catalogue entry and the count of catalogue
// requests. The add-on library and the catalogue's cache live in the test's
// data directory.
func addonScreenFixture(t *testing.T) (*gameShell, *nlScreen, modfetch.Entry, *atomic.Int32) {
	t.Helper()
	base, _ := modFixture(t)
	var archive bytes.Buffer
	if err := vfs.WriteArchive(&archive, []vfs.ArchiveFile{{Path: "units/armfark.fbi", Data: []byte("[UNITINFO]{UnitName=ARMFARK;Name=FARK;}")}}, vfs.ArchiveWriteOptions{}); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(modlibrary.Metadata{Schema: 1, ID: "bonus", Name: "Bonus units", Version: "original"})
	var zipped bytes.Buffer
	z := zip.NewWriter(&zipped)
	for name, data := range map[string][]byte{modlibrary.MetadataFile: meta, "AFark.ufo": archive.Bytes()} {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write(data)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	entry := modfetch.Entry{ID: "bonus", Name: "Bonus units", Version: "original", Summary: "An assist-repair Kbot.",
		Archive:  modfetch.Archive{URL: "/bonus.zip", Size: int64(zipped.Len()), SHA256: fmt.Sprintf("%x", sha256.Sum256(zipped.Bytes()))},
		Contents: []modfetch.Content{{Name: "AFark.ufo", Size: int64(archive.Len()), SHA256: fmt.Sprintf("%x", sha256.Sum256(archive.Bytes()))}}}
	var manifests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			manifests.Add(1)
			_ = json.NewEncoder(w).Encode(modfetch.Manifest{Schema: 1, Addons: []modfetch.Entry{entry}})
		case "/bonus.zip":
			_, _ = w.Write(zipped.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { addonDownload.cancelNetwork() })
	entry.Archive.URL = server.URL + entry.Archive.URL
	t.Setenv("NANOLATHE_ADDON_CATALOG", server.URL+"/manifest.json")
	g, s := settingsRegressionScreen(nil, settings.Defaults())
	g.cs.root, g.cs.baseRoots = base, []string{base}
	t.Cleanup(func() {
		s.addonList.stop()
		s.addonList.wait()
	})
	return g, s, entry, &manifests
}

// getFixtureAddon gets the fixture's add-on through the card and waits for
// the install, which switches it on in the draft (§5.7).
func getFixtureAddon(t *testing.T, s *nlScreen, entry modfetch.Entry) {
	t.Helper()
	s.getAddon(entry)
	for deadline := time.Now().Add(10 * time.Second); addonDownload.view().running; {
		if time.Now().After(deadline) {
			t.Fatal("the add-on download did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.pollAddons()
	s.addonList.wait()
}

// The card asks the catalogue only once it is shown (D5); a Get installs the
// add-on and switches it on in the draft, and Apply reloads the content with
// the drafted selection, keeping the running mod and the saved mod choice
// (DESIGN_MODS_MUTATORS §5.7, §8.2).
func TestNLScreenAddonGetSwitchesOnForApply(t *testing.T) {
	g, s, entry, manifests := addonScreenFixture(t)
	if manifests.Load() != 0 {
		t.Fatal("the catalogue was fetched before the card showed")
	}
	s.wantAddons()
	s.addonList.wait()
	rows, loading := s.addonList.view()
	if manifests.Load() != 1 || loading || len(rows) != 1 || rows[0].known.installed != nil || rows[0].known.entry == nil || rows[0].inFolder {
		t.Fatalf("listing after %d catalogue requests: loading %v, rows %+v", manifests.Load(), loading, rows)
	}
	getFixtureAddon(t, s, entry)
	if want := "bonus@original"; s.draft.addons != want || !s.touched["addons"] || s.dirty() != 1 {
		t.Fatalf("the install left the draft %q (touched %v, %d changed), want %q; failure %q, toast %q", s.draft.addons, s.touched["addons"], s.changed, want, s.addonList.failure, s.toast)
	}
	rows, _ = s.addonList.view()
	if len(rows) != 1 || rows[0].known.installed == nil || len(rows[0].units) != 1 || rows[0].units[0].key != "armfark" || rows[0].names != "FARK" {
		t.Fatalf("the library was not listed again: %+v", rows)
	}
	s.apply()
	if s.reload == nil {
		t.Fatal("Apply did not reload for the add-on")
	}
	r := s.reload.request
	if r.selector != "none" || r.mod != g.modSetting || r.addons == nil || len(*r.addons) != 1 || (*r.addons)[0] != (settings.AddonSelection{ID: "bonus", Version: "original"}) {
		t.Fatalf("reload request %+v, add-ons %v", r, r.addons)
	}
}

// The row's switch edits only the draft: turning it back off leaves nothing
// to apply, Apply then saves without a reload, and a mod that leaves add-ons
// off is named in the card's note.
func TestNLScreenAddonSwitchPointer(t *testing.T) {
	oldW, oldH := nlScreenW, nlScreenH
	nlScreenW, nlScreenH = 1560, 900
	t.Cleanup(func() { nlScreenW, nlScreenH = oldW, oldH })
	g, s, entry, _ := addonScreenFixture(t)
	s.wantAddons()
	s.addonList.wait()
	getFixtureAddon(t, s, entry)
	// The install is applied: the running content mounts it.
	g.cs.addonSelection = []settings.AddonSelection{{ID: "bonus", Version: "original"}}
	s.draft, s.touched = s.freshDraft(g), map[string]bool{}
	s.fonts, s.art = screenkit.LoadFonts(), &nlArt{}
	screen := ebiten.NewImage(1560, 900)
	defer screen.Deallocate()
	draw := func() {
		s.hits.Begin()
		s.heroAddons(screen, 100, 200, 900, 1)
		s.hits.End()
	}
	click := func(x, y float64) {
		s.hits.Update(screenkit.Input{X: x, Y: y, Pressed: true}, 0)
		s.hits.Update(screenkit.Input{X: x, Y: y, Released: true}, 0)
		draw()
	}
	draw()
	id := s.ui.id("addon-sw-", "bonus", 0, -1)
	var hit []float64
	for x := 100.5; x < 800; x++ {
		s.hits.Update(screenkit.Input{X: x, Y: 233}, 0)
		if s.hits.Hot() == id {
			hit = append(hit, x)
		}
	}
	if len(hit) == 0 {
		t.Fatal("the row has no switch")
	}
	x := hit[len(hit)/2]
	click(x, 233)
	if s.draft.addons != "" || s.dirty() != 1 {
		t.Fatalf("switching off left the draft %q with %d changed", s.draft.addons, s.changed)
	}
	click(x, 233)
	if s.draft.addons != "bonus@original" || s.dirty() != 0 {
		t.Fatalf("switching on again left the draft %q with %d changed", s.draft.addons, s.changed)
	}
	s.apply()
	if s.reload != nil {
		t.Fatal("an unchanged selection reloaded the content")
	}
	off := installFixtureConfig(t, mustModLibrary(t), "no-addons", `{"schema":2,"id":"no-addons","name":"Plain","version":"1","content":{"addons":false}}`)
	s.mods = []modlibrary.Mod{off}
	s.draft.mod = 1
	if note := s.cardNote(s.addonsCard(), 1); !strings.Contains(note, "Plain leaves the official add-ons off") {
		t.Fatalf("note %q", note)
	}
}

func mustModLibrary(t *testing.T) *modlibrary.Library {
	t.Helper()
	lib, err := openModLibrary()
	if err != nil {
		t.Fatal(err)
	}
	return lib
}

// A model-only add-on is described as model replacements, without a unit
// count or empty unit strip. The same row is used for a library copy and a
// byte-identical copy in the base folder, which still needs the installed
// selection for the model tier (§5.7).
func TestNLScreenAddonRowDescribesModelReplacements(t *testing.T) {
	dir := addonPackage(t, "models", "Models", map[string]string{"Objects3D/ARMCOM.3DO": "model"})
	path := filepath.Join(dir, "models.ufo")
	data := addonArchive(t, map[string]string{"Objects3D/ARMCOM.3DO": "model"})
	archive := addonlibrary.Archive{Name: "models.ufo", Size: int64(len(data)), SHA256: sha256.Sum256(data)}
	known := knownAddon{id: "models", version: "1", name: "Models", archives: []addonlibrary.Archive{archive}, installed: &addonlibrary.Addon{Paths: []string{path}}}
	s := &nlScreen{}
	for _, root := range []string{"", dir} {
		rows := nlAddonRowsOf([]knownAddon{known}, root)
		if len(rows) != 1 || !rows[0].models || len(rows[0].units) != 0 || rows[0].names != "" || rows[0].faction != "" || s.addonSubText(rows[0]) != "Model replacements" {
			t.Fatalf("model row under %q: %+v", root, rows)
		}
		if rows[0].inFolder || rows[0].known.installed == nil {
			t.Fatalf("model row under %q hides its installed switch: %+v", root, rows[0])
		}
	}
	// Without the library copy, the catalogue still needs to offer Get so
	// the model pack can be installed and selected at its layer.
	known.installed = nil
	known.entry = &modfetch.Entry{ID: "models", Name: "Models", Version: "1"}
	rows := nlAddonRowsOf([]knownAddon{known}, dir)
	if len(rows) != 1 || rows[0].inFolder || !rows[0].models || rows[0].known.installed != nil || rows[0].known.entry == nil {
		t.Fatalf("folder-only model row hides Get: %+v", rows)
	}
	if text := s.addonsCard().desc(nil, 0); !strings.Contains(text, "model replacements") {
		t.Fatalf("add-ons description %q", text)
	}
}

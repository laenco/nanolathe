package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/addonlibrary"
	contentprofiles "github.com/nanolathe-gg/nanolathe/internal/content/profiles"
	"github.com/nanolathe-gg/nanolathe/internal/settings"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

func addonOptions(ids ...string) Options {
	opts := Options{AddonsSet: true}
	for _, id := range ids {
		opts.Addons = append(opts.Addons, settings.AddonSelection{ID: id, Version: "1"})
	}
	return opts
}

func addonIDs(addons []addonlibrary.Addon) string {
	var ids []string
	for _, a := range addons {
		ids = append(ids, a.ID)
	}
	return strings.Join(ids, ",")
}

// Revisions retain their existing patch-replacement contract even when a
// revision happens to contain models only; otherwise audited content selects
// the model layer rather than a package's name (§5.7).
func TestAddonModelLayerClassification(t *testing.T) {
	plain := addonlibrary.Addon{ID: "arbitrary", Archives: []addonlibrary.Archive{{Name: "ordinary.ufo"}}}
	fix := addonlibrary.Addon{Archives: []addonlibrary.Archive{{Name: "rev31.gp3"}}}
	for _, tc := range []struct {
		addon            addonlibrary.Addon
		declared, models bool
		want             addonLayer
	}{
		{plain, false, false, layerContent},
		{plain, false, true, layerModel},
		{fix, false, true, layerFix},
		{fix, true, false, layerFaction},
	} {
		if got := addonLayerOf(tc.addon, tc.declared, tc.models); got != tc.want {
			t.Errorf("addonLayerOf(%+v, %v, %v) = %v; want %v", tc.addon, tc.declared, tc.models, got, tc.want)
		}
	}
}

// Selection order cannot change the layers: content, fix, models, faction,
// then the selected mod. Only a model-only payload can replace earlier
// models, two model packs remain alternatives, and factions retain their
// path isolation (§5.7 "Layers and mounting", §5.8).
func TestResolveAddonsOrdersModelLayerAndKeepsIsolation(t *testing.T) {
	base, _ := modFixture(t)
	faction := xenoFiles()
	faction["objects3d/xenotank.3do"] = "faction"
	packages := map[string]string{
		"official": addonPackage(t, "official", "Official", map[string]string{"units/aaa.fbi": "[UNITINFO]{UnitName=aaa;}", "objects3d/shared.3do": "official"}),
		"fix":      fixPackage(t, "fix", "Fix", map[string]string{"objects3d/shared.3do": "fix"}),
		"models":   addonPackage(t, "models", "Models", map[string]string{"objects3d/shared.3do": "models"}),
		"other":    addonPackage(t, "other", "Other models", map[string]string{"Objects3D/SHARED.3DO": "other models"}),
		"mixed":    addonPackage(t, "mixed", "Mixed", map[string]string{"objects3d/shared.3do": "mixed", "textures/extra.gaf": "texture"}),
		"xeno":     addonPackage(t, "xeno", "Xeno", faction),
	}
	for _, id := range []string{"official", "fix", "models", "other", "mixed", "xeno"} {
		if err := runInstallAddon(Options{InstallAddon: packages[id]}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	resolve := func(ids ...string) addonMount {
		t.Helper()
		out, err := resolveAddons(addonOptions(ids...), contentprofiles.Retail(), []string{base}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := resolve("xeno", "models", "fix", "official")
	if got := addonIDs(out.mounted); got != "official,fix,models,xeno" || len(out.notes) != 0 || len(out.models) != 1 || len(out.factions) != 1 {
		t.Fatalf("mounted %s; models %v, factions %v, notes %q", got, out.models, out.factions, out.notes)
	}
	// The production content mount gives the model tier the winning bytes.
	opts := addonOptions("xeno", "models", "fix", "official")
	opts.Root = base
	cs, err := openContent(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if data, err := cs.fs.ReadFileLimit("objects3d/shared.3do", 1024); err != nil || string(data) != "models" {
		t.Fatalf("shared model = %q, %v; want models", data, err)
	}
	sources := cs.unmappedMount.Sources("objects3d/shared.3do")
	if len(sources) != 3 || filepath.Base(sources[0].Source.SourcePath) != "models.ufo" || filepath.Base(sources[1].Source.SourcePath) != "rev31.gp3" || filepath.Base(sources[2].Source.SourcePath) != "official.ufo" {
		t.Fatalf("shared model sources = %+v", sources)
	}
	factions := cs.unmappedMount.Sources("objects3d/xenotank.3do")
	if len(factions) != 1 || factions[0].Source.Priority <= sources[0].Source.Priority {
		t.Fatalf("faction priority %+v is not above models %+v", factions, sources)
	}
	// Same-layer collisions preserve the first selected pack in either order.
	for _, ids := range [][]string{{"models", "other"}, {"other", "models"}} {
		got := resolve(ids...)
		if addonIDs(got.mounted) != ids[0] || len(got.notes) != 1 || !strings.Contains(got.notes[0], "also supplies") {
			t.Fatalf("two model packs %v: mounted %s, notes %q", ids, addonIDs(got.mounted), got.notes)
		}
	}
	// A mixed archive remains ordinary content, so it cannot use the model
	// layer to overwrite another content add-on.
	if got := resolve("official", "mixed"); addonIDs(got.mounted) != "official" || len(got.notes) != 1 {
		t.Fatalf("mixed payload: mounted %s, notes %q", addonIDs(got.mounted), got.notes)
	}
	// A faction cannot collide with models even though it mounts above them.
	clobber := xenoFiles()
	clobber["objects3d/shared.3do"] = "faction collision"
	if err := runInstallAddon(Options{InstallAddon: addonPackage(t, "clobber", "Clobber", clobber)}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := resolve("clobber", "models"); addonIDs(got.mounted) != "models" || len(got.notes) != 1 || !strings.Contains(got.notes[0], "also supplies objects3d/shared.3do") {
		t.Fatalf("faction collision: mounted %s, notes %q", addonIDs(got.mounted), got.notes)
	}
}

// Model and faction tiers remain below loose files, and every mod archive
// remains above the whole base root (§5.7 "Layers and mounting").
func TestAddonModelTierRemainsBelowLooseAndMod(t *testing.T) {
	base := t.TempDir()
	writeModFixtureFile(t, base, "objects3d/shared.3do", "loose")
	modelPath := filepath.Join(t.TempDir(), "models.ufo")
	factionPath := filepath.Join(t.TempDir(), "faction.ufo")
	writeModFixtureFile(t, filepath.Dir(modelPath), filepath.Base(modelPath), string(addonArchive(t, map[string]string{"objects3d/shared.3do": "model"})))
	writeModFixtureFile(t, filepath.Dir(factionPath), filepath.Base(factionPath), string(addonArchive(t, map[string]string{"objects3d/shared.3do": "faction"})))
	models, factions := map[string]bool{modelPath: true}, map[string]bool{factionPath: true}
	plan := vfs.DefaultRetailMountPlan()
	for full, want := range map[string]vfs.MountTier{modelPath: plan.GP3 + 1, factionPath: plan.GP3 + 2} {
		if tier, ok := addonExtraTier(factions, models)(full); !ok || tier != want || tier >= plan.Loose {
			t.Fatalf("tier for %s = %v, %v; want %v below loose", full, tier, ok, want)
		}
	}
	root := vfs.GameRoot{Dir: base, Extra: []string{factionPath, modelPath}, ExtraTier: addonExtraTier(factions, models)}
	mod := t.TempDir()
	writeModFixtureFile(t, mod, "mod.hpi", string(addonArchive(t, map[string]string{"objects3d/shared.3do": "mod"})))
	for _, tc := range []struct {
		roots []vfs.GameRoot
		want  string
	}{{[]vfs.GameRoot{root}, "loose"}, {[]vfs.GameRoot{root, {Dir: mod}}, "mod"}} {
		fs := vfs.New()
		if err := fs.MountGameRoots(tc.roots); err != nil {
			fs.Close()
			t.Fatal(err)
		}
		data, err := fs.ReadFile("objects3d/shared.3do")
		fs.Close()
		if err != nil || string(data) != tc.want {
			t.Fatalf("shared model = %q, %v; want %s", data, err, tc.want)
		}
	}
}

// The selected installed model pack keeps its layer when byte-identical
// archives also sit in the base folder. Those UFO copies alone would lose
// to the base patch (§5.7 "Layers and mounting").
func TestAddonModelsAlreadyInFolderKeepModelLayer(t *testing.T) {
	base, _ := modFixture(t)
	files := map[string]string{"objects3d/shared.3do": "model"}
	if err := runInstallAddon(Options{InstallAddon: addonPackage(t, "models", "Models", files)}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	writeModFixtureFile(t, base, "MODELS.UFO", string(addonArchive(t, files)))
	writeModFixtureFile(t, base, "rev31.gp3", string(addonArchive(t, map[string]string{"objects3d/shared.3do": "patch"})))
	opts := addonOptions("models")
	opts.Root = base
	cs, err := openContent(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if addonIDs(cs.addons) != "models" {
		t.Fatalf("the folder copy suppressed the selected model pack: %+v", cs.addons)
	}
	if data, err := cs.fs.ReadFileLimit("objects3d/shared.3do", 1024); err != nil || string(data) != "model" {
		t.Fatalf("model with a folder copy = %q, %v; want model above patch", data, err)
	}
	sources := cs.unmappedMount.Sources("objects3d/shared.3do")
	if len(sources) != 2 || filepath.Base(sources[0].Source.SourcePath) != "models.ufo" || filepath.Base(sources[1].Source.SourcePath) != "rev31.gp3" {
		t.Fatalf("model providers with a folder copy = %+v", sources)
	}
}

// Save and replay restoration cannot treat a model UFO's folder copy as
// already mounted at the model tier. It must select the installed pack;
// ordinary content and revision archives still need no selection (§5.7).
func TestAddonModelRestoreSelectsInstalledPackDespiteFolderCopy(t *testing.T) {
	base, _ := modFixture(t)
	files := map[string]string{"objects3d/shared.3do": "model"}
	packages := map[string]string{
		"models": addonPackage(t, "models", "Models", files),
		"plain":  addonPackage(t, "plain", "Plain", map[string]string{"textures/extra.gaf": "texture"}),
		"fix":    fixPackage(t, "fix", "Fix", map[string]string{"objects3d/shared.3do": "patch"}),
	}
	for _, id := range []string{"models", "plain", "fix"} {
		if err := runInstallAddon(Options{InstallAddon: packages[id]}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	root, err := addonlibrary.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	var known []knownAddon
	for _, id := range []string{"models", "plain", "fix"} {
		addon, ok := libraryAddon(root, id, "1")
		if !ok {
			t.Fatalf("%s is not installed", id)
		}
		known = append(known, knownAddon{id: id, version: "1", archives: addon.Archives, installed: &addon})
		for _, archive := range addon.Archives {
			data, err := os.ReadFile(filepath.Join(addon.Dir, archive.Name))
			if err != nil {
				t.Fatal(err)
			}
			writeModFixtureFile(t, base, archive.Name, string(data))
		}
	}
	cs, err := openContent(Options{Root: base, AddonsSet: true})
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	wanted := addonIdentity{"models", "1", addonlibrary.Digest(known[0].archives)}
	if addonsSatisfied(cs, []addonIdentity{wanted}, known) {
		t.Fatal("the lower-tier folder copy satisfied a model selection")
	}
	selection, missing := addonSelectionFor(cs, []addonIdentity{wanted}, known)
	if missing != nil || len(selection) != 1 || selection[0] != (settings.AddonSelection{ID: "models", Version: "1"}) {
		t.Fatalf("restored selection %+v, missing %+v", selection, missing)
	}
	restored, err := openContent(Options{Root: base, AddonsSet: true, Addons: selection})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if !addonsSatisfied(restored, []addonIdentity{wanted}, known) {
		t.Fatal("the selected model pack did not satisfy the restored identity")
	}
	if data, err := restored.fs.ReadFileLimit("objects3d/shared.3do", 1024); err != nil || string(data) != "model" {
		t.Fatalf("restored model = %q, %v; want model above patch", data, err)
	}
	// A folder copy alone cannot recover this layer: it needs an install.
	known[0].installed = nil
	if _, missing := addonSelectionFor(cs, []addonIdentity{wanted}, known); missing == nil || *missing != wanted {
		t.Fatalf("folder-only model pack missing identity = %+v", missing)
	}
	for _, ordinary := range known[1:] {
		w := addonIdentity{ordinary.id, ordinary.version, addonlibrary.Digest(ordinary.archives)}
		if !addonInFolder(cs, w, known) {
			t.Fatalf("%s lost the ordinary content/fix folder-copy contract", ordinary.id)
		}
	}
}

// A model pack is rehomed under a mod's authored directory table in both
// production and compile-trial mounts, and a model supplied by that mod wins.
func TestAddonModelsFollowModLayout(t *testing.T) {
	base, mods := modFixture(t)
	if err := runInstallAddon(Options{InstallAddon: addonPackage(t, "models", "Models", map[string]string{"objects3d/shared.3do": "model"})}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	mod := installFixtureConfig(t, mods, "renamed", `{"schema":2,"id":"renamed","name":"Renamed","version":"1","content":{"layout":{"objects3d":"modelsM"}}}`)
	opts := addonOptions("models")
	opts.Root, opts.Mod, opts.ModSet = base, mod.ID, true
	check := func(want string) {
		t.Helper()
		cs, err := openContent(opts)
		if err != nil {
			t.Fatal(err)
		}
		defer cs.Close()
		if data, err := cs.fs.ReadFileLimit("objects3d/shared.3do", 1024); err != nil || string(data) != want {
			t.Fatalf("model through layout = %q, %v; want %s", data, err, want)
		}
		if data, err := cs.unmappedMount.ReadFile("modelsM/shared.3do"); err != nil || string(data) != want {
			t.Fatalf("model at authored path = %q, %v; want %s", data, err, want)
		}
		if _, err := cs.unmappedMount.Stat("objects3d/shared.3do"); err == nil {
			t.Fatal("the model archive still answered at its old directory")
		}
	}
	check("model")
	writeModFixtureFile(t, mod.Dir, "mod.hpi", string(addonArchive(t, map[string]string{"modelsM/shared.3do": "mod"})))
	check("mod")
	// The compile trial needs a model under the same layout. The base
	// fixture's model is under the retail directory, so a missing rehome
	// makes this trial fail its required model read.
	trialBase := filepath.Dir(authoredNLPreviewContent(t, "scratch", 100).unmappedMount.Providers()[0].ID)
	profile := contentprofiles.Retail()
	profile.Directories = map[string]string{"objects3d": "modelsM"}
	modelArchive := filepath.Join(t.TempDir(), "scratch.ufo")
	writeModFixtureFile(t, filepath.Dir(modelArchive), filepath.Base(modelArchive), string(addonArchive(t, map[string]string{"objects3d/scratch.3do": string(authoredScratchModel(t))})))
	addon := addonlibrary.Addon{Paths: []string{modelArchive}, Archives: []addonlibrary.Archive{{Name: "scratch.ufo"}}}
	if err := composeAddons(profile, []string{trialBase}, nil, []addonlibrary.Addon{addon}, nil, map[string]bool{modelArchive: true}); err != nil {
		t.Fatalf("compile trial did not use the rehomed model: %v", err)
	}
}

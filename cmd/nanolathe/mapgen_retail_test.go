//go:build retail

package main

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/mapgen"
	"github.com/nanolathe-gg/nanolathe/internal/maplibrary"
	"github.com/nanolathe-gg/nanolathe/internal/save"
	"github.com/nanolathe-gg/nanolathe/internal/session"
)

// A map made from a retail world plays a skirmish whose commanders stand on
// its starts; the battle's save and replay record the recipe, and on content
// without the map — the next run's — the save loads on the map made again
// from it, and the replay verifies there (docs/DESIGN_MAP_GENERATOR.md §10).
func TestGeneratedRetailMapPlaysAndReplays(t *testing.T) {
	replayTestSettings(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	base := replayTestOptions(t)
	plain := replayTestContent(t, base)
	worlds, err := mapgenWorlds(plain.fs, plain.mapRoots)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(worlds, func(w mapgenWorldInfo) bool { return strings.EqualFold(w.Planet, "archipelago") })
	if i < 0 {
		t.Fatalf("no Archipelago world among %d", len(worlds))
	}
	world, sources, err := mapgenWorld(context.Background(), plain.fs, worlds[i], maplibrary.MaxTNTBytes, nil)
	if err != nil {
		t.Fatal(err)
	}
	gen, err := mapgen.NewGenerator(world)
	if err != nil {
		t.Fatal(err)
	}
	gen.Workers = mapgenWorkers()
	m, err := gen.Generate(context.Background(), mapgen.Params{Players: 3, Size: 8, Water: 1, Relief: 2, Metal: 1, Seed: 99}, nil)
	if err != nil {
		t.Fatal(err)
	}
	made, err := newGeneratedMap(m, worlds[i].Planet, sources, mapgenName(worlds[i].Planet, m), "A retail test map")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	opts := replayTestOptions(t, "--map", made.recipe.Name, "--seed", "7", "--gameplay", "strict-3.1", "--replay-dir", dir)
	opts.generatedMaps = []*generatedMap{made}
	cs := replayTestContent(t, opts)
	w := openReplayWindow(t, opts, cs)
	b := w.shell.battle
	if b.replay == nil {
		t.Fatal("the battle on the generated map is not recording")
	}
	commanders := 0
	for _, u := range b.sess.Units.IterSliced() {
		if u == nil || !u.Alive || u.Def == nil || !strings.HasSuffix(strings.ToLower(u.Def.UnitName), "com") {
			continue
		}
		commanders++
		near := slices.ContainsFunc(m.Starts, func(st mapgen.Start) bool {
			dx, dz := int(u.X.Int())-st.X, int(u.Z.Int())-st.Z
			return dx*dx+dz*dz <= 64*64
		})
		if !near {
			t.Errorf("player %d's commander stands at %d,%d, on none of the starts %+v", u.Owner, u.X.Int(), u.Z.Int(), m.Starts)
		}
	}
	if commanders < 2 {
		t.Fatalf("%d commanders on the generated map", commanders)
	}
	final := playScriptedWindowBattle(t, w, 300)
	saved := session.RetailSavePath(t.TempDir(), "generated")
	if err := w.shell.writeSaveWithSidecar(saved, func() error { return w.shell.writeBattleSave(saved, "generated") }); err != nil {
		t.Fatal(err)
	}
	sc, ok, err := save.ReadSidecar(saved)
	var recorded mapgenRecipe
	if err != nil || !ok || sc.Schema != save.SidecarSchemaGeneratedMap || json.Unmarshal(sc.GeneratedMap, &recorded) != nil || !reflect.DeepEqual(recorded, made.recipe) {
		t.Fatalf("the save's sidecar (schema %d, %v, %v) records %s, want the recipe", sc.Schema, ok, err, sc.GeneratedMap)
	}
	w.shell.teardownBattle(w.cl)

	listing, data := onlyReplay(t, dir)
	config, err := listing.Header.Configuration()
	if err != nil {
		t.Fatal(err)
	}
	recipe := config.Request().GeneratedMap
	if want := cs.generatedRecipeRecord(made.recipe.Name); len(recipe) == 0 || string(recipe) != string(want) {
		t.Fatalf("the replay records the recipe %s, want %s", recipe, want)
	}

	// The next run: a shell on content without the map loads the save, which
	// makes the map again from its recipe first.
	nextOpts := replayTestOptions(t, "--map", "ashap plateau", "--seed", "7", "--gameplay", "strict-3.1")
	next := replayTestContent(t, nextOpts)
	n := openReplayWindow(t, nextOpts, next)
	n.shell.teardownBattle(n.cl)
	if n.shell.hasSkirmishMap(made.recipe.Name) {
		t.Fatal("the next run's content already has the generated map")
	}
	if err := n.shell.loadRetailSavePath(saved); err == nil || !strings.Contains(loadFailureMessage(err), "--map") {
		t.Fatalf("a --map view loaded a save whose generated map it lacks: %v", err)
	}
	n.shell.opts.Map = ""
	if err := n.shell.loadRetailSavePath(saved); err != nil {
		t.Fatalf("load the save on the generated map: %v", err)
	}
	if n.shell.battle == nil || !strings.EqualFold(n.shell.battle.sess.Skirmish.MapName, made.recipe.Name) {
		t.Fatal("the save did not load on its generated map")
	}
	v, err := verifyReplay(data, n.shell.cs)
	if err != nil {
		t.Fatal(err)
	}
	if v.Final != final || v.Truncated {
		t.Fatalf("verification %+v on the map made again", v)
	}
}

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/settings"
)

// Each row's chosen point persists in the settings file as `start` with the
// map it was placed on, omitted for Auto; a file whose points belong to
// another map loads them as Auto; and the point travels with its row through
// the start conversion, which compacts the open rows out
// (DESIGN_INTERFACE_HUD_INPUT §2.6 "Start positions").
func TestTheSettingsFileKeepsEachRowsStartPoint(t *testing.T) {
	g := &gameShell{maps: []string{"Ashap Plateau"}, setup: newSkirmishMenuConfig("m")}
	blob := settings.Defaults()
	blob.Skirmish.Map, blob.Skirmish.StartMap = "Ashap Plateau", "ashap plateau"
	blob.Skirmish.NumPlayers = 3
	blob.Skirmish.Players[0] = settings.Player{Controller: 1, AllyGroup: 5, Metal: 1000, Energy: 1000, Start: &settings.StartPoint{X: 300, Z: 400}}
	blob.Skirmish.Players[1] = settings.Player{Controller: 0, AllyGroup: 5, Metal: 1000, Energy: 1000, Start: &settings.StartPoint{X: 10, Z: 20}}
	blob.Skirmish.Players[2] = settings.Player{Controller: 2, Side: 1, Color: 2, AllyGroup: 5, Metal: 1000, Energy: 1000}
	g.applySettings(blob)
	if p := g.setup.Players[0].Start; !p.Set || p.X != 300 || p.Z != 400 {
		t.Fatalf("loaded row 0 start %+v", p)
	}
	g.setup.MapName = "Ashap Plateau"
	back := g.captureSettings()
	if p := back.Skirmish.Players[0].Start; p == nil || p.X != 300 || p.Z != 400 || back.Skirmish.StartMap != "Ashap Plateau" {
		t.Fatalf("captured row 0 start %+v on %q", p, back.Skirmish.StartMap)
	}
	if data, _ := json.Marshal(back.Skirmish.Players[2]); strings.Contains(string(data), `"start"`) {
		t.Fatalf("an Auto row wrote %s", data)
	}
	cfg := g.skirmishConfigForStart("Ashap Plateau")
	if cfg.NumPlayers != 2 || !cfg.Players[0].Start.Set || cfg.Players[1].Start.Set {
		t.Fatalf("start conversion: %d players, starts %+v %+v; want the open row's point dropped with it", cfg.NumPlayers, cfg.Players[0].Start, cfg.Players[1].Start)
	}
	blob.Skirmish.StartMap = "Another Map"
	g.applySettings(blob)
	if g.setup.Players[0].Start.Set {
		t.Fatal("a point placed on another map loaded")
	}
	bad := settings.Skirmish{Players: []settings.Player{{Start: &settings.StartPoint{X: settings.StartLimit}}, {Start: &settings.StartPoint{Z: -1}}}}
	bad.Normalize()
	if bad.Players[0].Start != nil || bad.Players[1].Start != nil {
		t.Fatal("out-of-range points survived Normalize")
	}
}

// A removed map must not transfer its chosen points to the installed fallback,
// including when another preference change writes the setup back to disk.
func TestMissingSavedMapDropsChosenStarts(t *testing.T) {
	g := &gameShell{maps: []string{"Installed"}, setup: newSkirmishMenuConfig("Installed")}
	s := settings.Defaults()
	s.Skirmish.Map, s.Skirmish.StartMap = "Removed", "Removed"
	s.Skirmish.Players[0].Start = &settings.StartPoint{X: 300, Z: 400}
	g.applySettings(s)
	if g.setup.MapName != "Installed" || g.setup.Players[0].Start.Set {
		t.Fatalf("fallback %q has start %+v from a removed map", g.setup.MapName, g.setup.Players[0].Start)
	}
	back := g.captureSettings()
	if back.Skirmish.StartMap != "" || back.Skirmish.Players[0].Start != nil {
		t.Fatalf("fallback saved another map's chosen point: %+v", back.Skirmish)
	}
}

// The Place Starts button is offered under Modern and not under Strict 3.1,
// whose battle entry ignores chosen points (SeatRules.ChosenStarts).
func TestPlaceStartsFollowsTheRules(t *testing.T) {
	g := &gameShell{setup: newSkirmishMenuConfig("m")}
	g.gameplay = gameplay.Modern
	if !g.setupSeatRules().ChosenStarts() {
		t.Fatal("Modern does not offer chosen starts")
	}
	g.gameplay = gameplay.Strict31
	if g.setupSeatRules().ChosenStarts() {
		t.Fatal("Strict 3.1 offers chosen starts")
	}
	g.setup.Players[1].Start = session.StartPoint{Set: true, X: 1, Z: 2}
	g.clearChosenStarts()
	if g.setup.Players[1].Start.Set {
		t.Fatal("clearChosenStarts kept a point")
	}
}

// setSetupMap is how the setup's map changes, from the map picker, the
// online room's picker and the deletion of the selected downloaded map: a
// different map clears the chosen points, and the same map, in any letter
// case, keeps them.
func TestSetSetupMapClearsPointsOnlyForAnotherMap(t *testing.T) {
	g := &gameShell{setup: newSkirmishMenuConfig("Ashap Plateau")}
	g.setup.Players[0].Start = session.StartPoint{Set: true, X: 300, Z: 400}
	g.setSetupMap("ashap plateau")
	if !g.setup.Players[0].Start.Set || g.setup.MapName != "ashap plateau" {
		t.Fatalf("the same map dropped the point: %q %+v", g.setup.MapName, g.setup.Players[0].Start)
	}
	g.setSetupMap("Another Map")
	if g.setup.Players[0].Start.Set || g.setup.MapName != "Another Map" {
		t.Fatalf("another map kept the point: %q %+v", g.setup.MapName, g.setup.Players[0].Start)
	}
}

//go:build retail

package main

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/headless"
	"github.com/nanolathe-gg/nanolathe/internal/mission"
	"github.com/nanolathe-gg/nanolathe/internal/session"
)

// Eight players on a retail map with fewer than eight start positions, of
// which the stock set has one, Luschinfloggen with six
// (docs/DESIGN_SESSIONS_AI_SAVE.md "Modern extra start positions"): Strict
// 3.1 refuses the setup with retail's message, and Modern seats it — every
// commander is created, the players the map has no start for begin at
// generated starts well apart from everyone, and the same seed composes the
// same starts.
func TestEightPlayersOnASmallMapRetail(t *testing.T) {
	resetSaveLoadScreenState(t)
	shell, _ := retailShellForTest(t)
	shell.settingsWritable = false
	small := ""
	for _, name := range shell.maps {
		d := shell.mapDataFor(name)
		if d == nil || d.ota == nil || d.tnt == nil {
			continue
		}
		if schema, err := mission.SelectNetworkSchema(d.ota, 99); err == nil && schema.StartPositions >= 2 && schema.StartPositions < 8 {
			small = name
			break
		}
	}
	if small == "" {
		t.Skip("no retail map with two to seven start positions")
	}
	shell.activateGadget("SINGLE")
	shell.activateGadget("Skirmish")
	shell.setup.MapName = small
	shell.setup.NumPlayers = 8
	shell.retailControllers = [session.SkirmishMaxPlayers]int{1, 2, 2, 2, 2, 2, 2, 2}
	for i := 0; i < 8; i++ {
		shell.setup.Players[i].Color = i
		shell.setup.Players[i].AllyGroup = session.SkirmishDefaultAllyGroup
		shell.setup.Players[i].Start = session.StartPoint{}
	}
	shell.fillShownRowResources()
	shell.openMenu(modeMenuSkirmish)
	shell.gameplay = gameplay.Strict31
	if msg := shell.retailSkirmishStartError(); msg != "There are too many players enabled for this map" {
		t.Fatalf("Strict 3.1 on %s: preflight %q, want retail's refusal", small, msg)
	}
	shell.gameplay = gameplay.Modern
	if msg := shell.retailSkirmishStartError(); msg != "" {
		t.Fatalf("Modern on %s: preflight %q, want none", small, msg)
	}
	compose := func() [8][2]int32 {
		cfg := shell.skirmishConfigForStart(small)
		opts := shell.opts
		opts.Gameplay = gameplay.Modern
		request, err := skirmishBattleRequest(opts, shell.cs, cfg, headlessScenarioSkirmish, nil, newBattleSeedSource(Options{Seed: 3}))
		if err != nil {
			t.Fatal(err)
		}
		battle, err := headless.ComposeFreshBattle(request.value)
		if err != nil {
			t.Fatalf("Modern: eight players on %s do not compose: %v", small, err)
		}
		var at [8][2]int32
		var seen [8]bool
		for _, u := range battle.Session.Units.Iter() {
			if u.Owner < 8 {
				at[u.Owner], seen[u.Owner] = [2]int32{int32(u.X >> 16), int32(u.Z >> 16)}, true
			}
		}
		for i, ok := range seen {
			if !ok {
				t.Fatalf("Modern on %s: player %d has no commander (%v)", small, i, at)
			}
		}
		return at
	}
	at := compose()
	for i := range at {
		for j := i + 1; j < len(at); j++ {
			dx, dz := int64(at[i][0]-at[j][0]), int64(at[i][1]-at[j][1])
			if dx*dx+dz*dz < 128*128 {
				t.Fatalf("Modern on %s: players %d and %d begin %v and %v, within 128 pixels", small, i, j, at[i], at[j])
			}
		}
	}
	if again := compose(); again != at {
		t.Fatalf("Modern on %s: the same seed composed %v then %v", small, at, again)
	}
	t.Logf("%s: eight starts %v", small, at)
}

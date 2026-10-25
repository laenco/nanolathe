//go:build retail

package main

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/client"
	"github.com/nanolathe-gg/nanolathe/internal/frame"
	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/headless"
	"github.com/nanolathe-gg/nanolathe/internal/input"
	"github.com/nanolathe-gg/nanolathe/internal/session"
)

// The Players button on the retail art: clicks raise the shown rows to
// eight, a right click lowers them, and an eight-player setup — one human
// and seven computers — composes with eight commanders under Strict 3.1 and
// Modern (DESIGN_INTERFACE_HUD_INPUT §2.6 "Player count"). With
// NANOLATHE_SHOT_DIR set, the eight-row screen is written there as a PNG.
func TestSkirmishScreenSetsEightPlayersRetail(t *testing.T) {
	resetSaveLoadScreenState(t)
	shell, _ := retailShellForTest(t)
	shell.settingsWritable = false
	shot := survivalMenuShooter(t, shell)
	cl, err := client.New(client.Options{Buffer: &frame.Buffer{}, Width: 640, Height: 480})
	if err != nil {
		t.Fatal(err)
	}
	click := func(name string, button input.MouseButton) {
		clickRowGadget(t, shell, shell.activePanel(), cl, name, button)
	}
	shell.activateGadget("SINGLE")
	shell.activateGadget("Skirmish")
	shell.setup.NumPlayers = 4
	shell.openMenu(modeMenuSkirmish)
	for range 4 {
		click(skirmishPlayersButton, input.MouseButtonLeft)
	}
	p := shell.activePanel()
	if shell.setup.NumPlayers != 8 || p.TextAt(p.Index(skirmishPlayersButton)) != "8" || p.Index("Player7") < 0 || p.Index("Player8") >= 0 {
		t.Fatalf("four clicks from 4: count %d, caption %q, Player7 %d, Player8 %d", shell.setup.NumPlayers, p.TextAt(p.Index(skirmishPlayersButton)), p.Index("Player7"), p.Index("Player8"))
	}
	click(skirmishPlayersButton, input.MouseButtonRight)
	if shell.setup.NumPlayers != 7 {
		t.Fatalf("right click from 8 gave %d rows, want 7", shell.setup.NumPlayers)
	}
	click(skirmishPlayersButton, input.MouseButtonLeft)
	shell.retailControllers = [session.SkirmishMaxPlayers]int{1, 2, 2, 2, 2, 2, 2, 2}
	for i := 0; i < 8; i++ {
		shell.setup.Players[i].Color = i
		shell.setup.Players[i].AllyGroup = session.SkirmishDefaultAllyGroup
	}
	shell.refreshRetailPanel()
	if msg := shell.retailSkirmishStartError(); msg != "" {
		t.Skipf("map %q cannot seat eight: %s", shell.setup.MapName, msg)
	}
	shot("skirmish-eight-players")
	for _, mode := range []gameplay.Mode{gameplay.Strict31, gameplay.Modern} {
		cfg := shell.skirmishConfigForStart(shell.setup.MapName)
		opts := shell.opts
		opts.Gameplay = mode
		request, err := skirmishBattleRequest(opts, shell.cs, cfg, headlessScenarioSkirmish, nil, newBattleSeedSource(Options{Seed: 3}))
		if err != nil {
			t.Fatal(err)
		}
		battle, err := headless.ComposeFreshBattle(request.value)
		if err != nil {
			t.Fatalf("%s: the eight-player setup does not compose: %v", mode, err)
		}
		var owners [session.SkirmishMaxPlayers]int
		for _, u := range battle.Session.Units.Iter() {
			owners[u.Owner]++
		}
		for i := 0; i < 8; i++ {
			if owners[i] != 1 {
				t.Fatalf("%s: player %d has %d units at entry, want its commander (%v)", mode, i, owners[i], owners)
			}
		}
		if cfg.NumPlayers != 8 {
			t.Fatalf("%s: %d players composed, want 8", mode, cfg.NumPlayers)
		}
	}
}

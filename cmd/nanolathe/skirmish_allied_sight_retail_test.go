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
	"github.com/nanolathe-gg/nanolathe/internal/visibility"
)

// The skirmish screen's Allied Sight button on the retail art: a click turns
// it on, and the battle the screen starts — the human and a computer on one
// team, a second computer opposed — gives the human its teammate's sight
// under Modern, while Strict 3.1 keeps retail's separate sight
// (DESIGN_INTERFACE_HUD_INPUT §2.6 "Allied sight"). With NANOLATHE_SHOT_DIR
// set, the screen is written as a PNG.
func TestSkirmishScreenSharesAlliedSightRetail(t *testing.T) {
	resetSaveLoadScreenState(t)
	shell, _ := retailShellForTest(t)
	shell.settingsWritable = false
	shot := survivalMenuShooter(t, shell)
	cl, err := client.New(client.Options{Buffer: &frame.Buffer{}, Width: 640, Height: 480})
	if err != nil {
		t.Fatal(err)
	}
	shell.activateGadget("SINGLE")
	shell.activateGadget("Skirmish")
	shell.setup.NumPlayers = 3
	shell.setup.AlliedSight = false
	shell.retailControllers = [session.SkirmishMaxPlayers]int{1, 2, 2}
	for i, group := range []int{0, 0, 1} {
		shell.setup.Players[i].AllyGroup = group
		shell.setup.Players[i].Color = i
		shell.setup.Players[i].Start = session.StartPoint{}
	}
	shell.openMenu(modeMenuSkirmish)
	clickRowGadget(t, shell, shell.activePanel(), cl, alliedSightButton, input.MouseButtonLeft)
	p := shell.activePanel()
	if !shell.setup.AlliedSight || p.StageAt(p.Index(alliedSightButton)) != 1 {
		t.Fatalf("a click left Allied Sight %v at stage %d", shell.setup.AlliedSight, p.StageAt(p.Index(alliedSightButton)))
	}
	if msg := shell.retailSkirmishStartError(); msg != "" {
		t.Skipf("map %q cannot start: %s", shell.setup.MapName, msg)
	}
	shot("skirmish-allied-sight")
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
			t.Fatalf("%s: the setup does not compose: %v", mode, err)
		}
		s := battle.Session
		if !s.Skirmish.AlliedSight {
			t.Fatalf("%s: the battle lost Allied Sight", mode)
		}
		for _, u := range s.Units.Iter() {
			want := u.Owner == 0 || (u.Owner == 1 && mode != gameplay.Strict31)
			if got := s.Vis.VisiblePoint(visibility.PlayerID(0), u.X, u.Y, u.Z); got != want {
				t.Fatalf("%s: the human sees player %d's commander %v, want %v", mode, u.Owner, got, want)
			}
		}
	}
}

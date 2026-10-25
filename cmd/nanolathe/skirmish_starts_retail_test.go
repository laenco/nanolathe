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

// The skirmish screen's Place Starts window on the retail art: it opens from
// its button, a click on the map places the selected player and moves on to
// the next, a right click on a marker puts the player back on Auto, and Done
// closes it. The battle the screen then starts puts the chosen commander on
// the nearest cell it can stand on under Modern, and on retail's start under
// Strict 3.1 (docs/DESIGN_SESSIONS_AI_SAVE.md "Modern chosen start
// positions", DESIGN_INTERFACE_HUD_INPUT §2.6 "Start positions"). With
// NANOLATHE_SHOT_DIR set, the open window and the map picker are written
// there as PNGs.
func TestSkirmishScreenPlacesStartsOnTheMapRetail(t *testing.T) {
	resetSaveLoadScreenState(t)
	shell, _ := retailShellForTest(t)
	shell.settingsWritable = false
	shell.gameplay = gameplay.Modern
	shot := survivalMenuShooter(t, shell)
	cl, err := client.New(client.Options{Buffer: &frame.Buffer{}, Width: 640, Height: 480})
	if err != nil {
		t.Fatal(err)
	}
	clickAt := func(x, y int32, button input.MouseButton) {
		mouse := cl.Input().Mouse
		mouse.SetPosition(float32(x), float32(y))
		mouse.SetButton(button, true)
		shell.menuInput(cl)
		mouse.ResetEdges()
		mouse.SetButton(button, false)
		shell.menuInput(cl)
		mouse.ResetEdges()
	}
	shell.activateGadget("SINGLE")
	shell.activateGadget("Skirmish")
	shell.setup.NumPlayers = 4
	shell.retailControllers = [session.SkirmishMaxPlayers]int{1, 2, 0, 0}
	shell.clearChosenStarts()
	shell.openMenu(modeMenuSkirmish)
	p := shell.activePanel()
	if !p.ActiveAt(p.Index(placeStartsButton)) || p.TextAt(p.Index(placeStartsButton)) != "Auto" {
		t.Fatalf("Place Starts offered %v with caption %q under Modern", p.ActiveAt(p.Index(placeStartsButton)), p.TextAt(p.Index(placeStartsButton)))
	}
	clickRowGadget(t, shell, p, cl, placeStartsButton, input.MouseButtonLeft)
	pl := shell.startPlacer
	if pl == nil || pl.selected != 0 {
		t.Fatalf("Place Starts opened %v", pl)
	}
	d := shell.mapDataFor(shell.setup.MapName)
	inner, ew, eh, ok := placerProjection(d, placerMap)
	if !ok {
		t.Fatalf("map %q has no preview", shell.setup.MapName)
	}
	sx, sy := inner.X+inner.W/3, inner.Y+inner.H/2
	wantX, wantZ := placerWorld(inner, ew, eh, sx, sy)
	clickAt(sx, sy, input.MouseButtonLeft)
	if got := shell.setup.Players[0].Start; got != (session.StartPoint{Set: true, X: wantX, Z: wantZ}) || pl.selected != 1 {
		t.Fatalf("a map click placed %+v and selected row %d; want (%d,%d) and row 1", got, pl.selected, wantX, wantZ)
	}
	clickAt(sx, sy, input.MouseButtonRight)
	if shell.setup.Players[0].Start.Set {
		t.Fatal("a right click on the marker kept the point")
	}
	clickAt(placerRowRect(0).X+10, placerRowRect(0).Y+10, input.MouseButtonLeft)
	clickAt(sx, sy, input.MouseButtonLeft)
	shot("skirmish-place-starts")
	clickAt(placerDone.X+10, placerDone.Y+10, input.MouseButtonLeft)
	if shell.startPlacer != nil || p.TextAt(p.Index(placeStartsButton)) != "1 Placed" {
		t.Fatalf("Done left the window %v and the caption %q", shell.startPlacer, p.TextAt(p.Index(placeStartsButton)))
	}
	retailStart := func(owner int) (int32, int32) {
		for _, sp := range mapStartPositions(d, 2) {
			if int(sp.ID) == owner {
				return int32(sp.X), int32(sp.Z)
			}
		}
		t.Fatalf("no StartPos%d", owner+1)
		return 0, 0
	}
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
		for _, u := range battle.Session.Units.Iter() {
			x, z := int32(u.X>>16), int32(u.Z>>16)
			if u.Owner == 0 && mode == gameplay.Modern {
				// The nearest standable cell: within a few cells of the click.
				if placerAbs(x-wantX) > 64 || placerAbs(z-wantZ) > 64 {
					t.Fatalf("Modern: the chosen commander is at (%d,%d), far from the chosen (%d,%d)", x, z, wantX, wantZ)
				}
				continue
			}
			rx, rz := retailStart(int(u.Owner))
			if x != rx || z != rz {
				t.Fatalf("%s: player %d commander at (%d,%d), want its StartPos%d (%d,%d)", mode, u.Owner, x, z, u.Owner+1, rx, rz)
			}
		}
	}
	shell.activateGadget("SelectMap")
	shot("map-start-positions")
	shell.activateGadget("PREVMENU")
}

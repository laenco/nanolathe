package main

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/settings"
)

// The skirmish setup's Allied Sight choice persists in the settings file as
// `alliedSight`, omitted when off, and is carried to battle in the setup
// (DESIGN_INTERFACE_HUD_INPUT §2.6 "Allied sight").
func TestTheSettingsFileKeepsAlliedSight(t *testing.T) {
	g := &gameShell{setup: newSkirmishMenuConfig("m")}
	blob := settings.Defaults()
	blob.Skirmish.AlliedSight = true
	g.applySettings(blob)
	if !g.setup.AlliedSight || !g.captureSettings().Skirmish.AlliedSight {
		t.Fatal("the settings file lost Allied Sight")
	}
	g.activateSkirmishGadget(alliedSightButton)
	if g.setup.AlliedSight || g.captureSettings().Skirmish.AlliedSight {
		t.Fatal("the button did not turn Allied Sight off")
	}
	g.activateSkirmishGadget(alliedSightButton)
	if cfg := g.skirmishConfigForStart("m"); !cfg.AlliedSight {
		t.Fatal("the start conversion dropped Allied Sight")
	}
}

// The online lobby's host keeps a skirmish room's teams' sight separate with
// the Allied Sight button, which writes required configuration extension 18; the default
// shares it, as online skirmishes always did, and a Survival room has no
// button (DESIGN_MULTIPLAYER §6.7 "Allied sight").
func TestOnlineLobbyHostChoosesAlliedSight(t *testing.T) {
	useOnlineSessionSeams(t, 4)
	g := onlineTestShell(t)
	fake := newFakeOnlineLobby(0, 0, 1)
	openTestLobby(t, g, fake, onlineTestCatalog())
	r := &g.online.room
	p := r.panel
	if !p.ActiveOf(alliedSightButton) || p.StageAt(p.Index(alliedSightButton)) != 1 || greyed(p, alliedSightButton) {
		t.Fatal("the host's skirmish room does not offer shared Allied Sight")
	}
	sight := func() session.MatchAlliedSight {
		c, err := session.DecodeMatchConfig(fake.config)
		if err != nil {
			t.Fatal(err)
		}
		return c.Request().AlliedSight
	}
	g.activateGadget(alliedSightButton)
	g.pollOnline()
	if got := sight(); got != session.MatchAlliedSightSeparate || !r.settings.separateSight || p.StageAt(p.Index(alliedSightButton)) != 0 {
		t.Fatalf("separate: extension 18 = %d, stage %d", got, p.StageAt(p.Index(alliedSightButton)))
	}
	if !onlineSettingsOf(mustDecodeRequest(t, fake.config)).separateSight {
		t.Fatal("a guest reads the room's sight as shared")
	}
	g.activateGadget(alliedSightButton)
	g.pollOnline()
	if got := sight(); got != session.MatchAlliedSightVersion1 || r.settings.separateSight {
		t.Fatalf("shared again: extension 18 = %d", got)
	}
	r.settings.survival = true
	g.refreshOnlineLobby()
	if p.ActiveOf(alliedSightButton) || p.ActiveOf(alliedSightLabel) {
		t.Fatal("a Survival room offers Allied Sight")
	}
	// A guest sees the host's choice but cannot change it.
	guest := onlineTestShell(t)
	openTestLobby(t, guest, newFakeOnlineLobby(1, 0, 1), onlineTestCatalog())
	if gp := guest.online.room.panel; !gp.ActiveOf(alliedSightButton) || !greyed(gp, alliedSightButton) {
		t.Fatal("a guest is offered the host's Allied Sight")
	}
}

func mustDecodeRequest(t *testing.T, payload []byte) session.MatchConfigRequest {
	t.Helper()
	c, err := session.DecodeMatchConfig(payload)
	if err != nil {
		t.Fatal(err)
	}
	return c.Request()
}

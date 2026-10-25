package session

import (
	"bytes"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/economy"
	"github.com/nanolathe-gg/nanolathe/internal/visibility"
)

// Allied Sight's teams are read from the alliance table (docs/DESIGN_SESSIONS_AI_SAVE.md
// "Modern allied sight"): mutual allies form a team, a player alone in its
// group and an absent slot join none. Membership is observed through the
// jammer exemption, which a vision team grants its members under every rule
// set and Strict 3.1 otherwise refuses [03 R-VIS-01 §5].
func TestAlliedSightTeamsFollowTheAllianceTable(t *testing.T) {
	s := &Session{Econ: &economy.Service{}, Vis: visibility.New(minimalTerrain(), 0)}
	s.Vis.Rules = visibility.StrictRules{}
	// Slots 0 and 2 are one team, 1 and 3 another, 4 is alone, and 5 is
	// allied with 4 but absent.
	group := []int{0, 1, 0, 1, 2, 2}
	for i, g := range group {
		s.Econ.Players[i] = economy.Player{Exists: i != 5, ControllerState: 2}
		for j, h := range group {
			s.Econ.Players[i].Allies[j] = g == h
		}
	}
	s.setAlliedVisionTeams()
	for _, c := range []struct {
		viewer, owner visibility.PlayerID
		team          bool
	}{{0, 2, true}, {2, 0, true}, {1, 3, true}, {0, 1, false}, {2, 3, false}, {4, 5, false}, {4, 0, false}} {
		if got := !s.Vis.JammerSuppresses(c.viewer, c.owner); got != c.team {
			t.Errorf("players %d and %d one team: %v, want %v", c.viewer, c.owner, got, c.team)
		}
	}
}

// Required extension 18 carries only explicit sight choices. The inherited
// default keeps version 2's existing encoding and historical sight behavior.
func TestAlliedSightIsRequiredExtension18(t *testing.T) {
	if !MatchAlliedSightVersion1.Shared(true) || MatchAlliedSightVersion1.Shared(false) ||
		MatchAlliedSightSeparate.Shared(true) || !MatchAlliedSightShared.Shared(false) {
		t.Fatal("extension 18 reads wrongly")
	}
	r := validMatchRequest(t)
	v1 := encodeMatchForTest(t, resolveMatch(t, r))
	r.AlliedSight = MatchAlliedSightSeparate
	v2 := encodeMatchForTest(t, resolveMatch(t, r))
	if !bytes.Equal(v2[len(v1):], []byte{18, 1, 1, 1, 0}) {
		t.Fatalf("extension tail %x, want required sight extension 18 then terminator", v2[len(v1):])
	}
	if !checkMatchDecode(t, v2) {
		t.Fatal("a separate-sight room did not decode")
	}
	r.AlliedSight = MatchAlliedSightShared + 1
	if _, err := ResolveMatchConfig(r); err == nil {
		t.Fatal("an unknown allied-sight word resolved")
	}
	s := validSurvivalRequest(t)
	s.AlliedSight = MatchAlliedSightSeparate
	if _, err := ResolveMatchConfig(s); err == nil {
		t.Fatal("a Survival room with separate sight resolved")
	}
	cfg := DirectSkirmishConfig("fixture")
	cfg.AlliedSight = true
	local, err := NewMatchConfigRequest(cfg, SkirmishEntryOptions{}, MatchRoomInputs{Policies: MatchPolicies{Revision: 1, Scheduling: 1, Pacing: 1, Drop: 1, Audience: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if local.AlliedSight != MatchAlliedSightShared {
		t.Fatalf("a shared single-player setup wrote extension 18 = %d", local.AlliedSight)
	}
	if back, _ := matchSkirmishSetup(&local); !back.AlliedSight {
		t.Fatal("the admitted setup lost Allied Sight")
	}
}

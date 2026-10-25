package session

import (
	"path/filepath"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/save"
	"github.com/nanolathe-gg/nanolathe/internal/testsupport/retailcat"
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"github.com/nanolathe-gg/nanolathe/internal/visibility"
)

// Allied Sight on the retail corpus: in a three-player skirmish on Ashap
// Plateau where the human and one computer are a team, the human sees its
// teammate's commander only with the option under Modern, never under
// Strict 3.1, which keeps retail's separate sight, and never the opponent's;
// and a save keeps the choice through its sidecar's flag, so the load sees
// the same (docs/DESIGN_SESSIONS_AI_SAVE.md "Modern allied sight").
func TestAlliedSightSharesATeamsSightRetail(t *testing.T) {
	cat, fs := retailcat.Shared(t)
	commander := func(s *Session, owner uint8) *units.Unit {
		for _, u := range s.Units.IterSliced() {
			if u != nil && u.Alive && u.Owner == owner {
				return u
			}
		}
		t.Fatalf("player %d has no commander", owner)
		return nil
	}
	sees := func(s *Session, owner uint8) bool {
		u := commander(s, owner)
		return s.Vis.VisiblePoint(visibility.PlayerID(0), u.X, u.Y, u.Z)
	}
	for _, rules := range []string{StrictRuleSetName, ModernRuleSetName} {
		for _, shared := range []bool{false, true} {
			cfg := SkirmishConfig{MapName: retailMap, NumPlayers: 3, RNGSimSeed: 12345, RNGCrtSeed: 67890}
			cfg.ApplyDefaults()
			cfg.Gameplay = gameplay.Mode(rules)
			for i, group := range []int{0, 0, 1} {
				cfg.Players[i].Side, cfg.Players[i].AllyGroup, cfg.Players[i].Controller = i&1, group, SkirmishControllerComputer
			}
			cfg.Players[0].Controller = SkirmishControllerHuman
			cfg.AlliedSight = shared
			s, err := NewSkirmishWithEntryOptions(fs, cat, cfg, SkirmishEntryOptions{})
			if err != nil {
				t.Fatalf("%s: compose: %v", rules, err)
			}
			if got, want := sees(s, 1), shared && rules != StrictRuleSetName; got != want {
				t.Fatalf("%s, allied sight %v: the human sees its teammate's commander %v, want %v", rules, shared, got, want)
			}
			if sees(s, 2) {
				t.Fatalf("%s, allied sight %v: the human sees the opponent's commander", rules, shared)
			}
			if !shared || rules != ModernRuleSetName {
				continue
			}
			// Through a save: the sidecar's flag restores the teams.
			if sc := SaveSidecar(s); !sc.AlliedSight {
				t.Fatal("the sidecar does not record Allied Sight")
			}
			s.State = StateBattle
			summary := RetailBattleSummary(s, "allied sight", "0", s.Skirmish.UnitLimit)
			inputs, err := s.RetailBattleSaveInputs(summary, save.Camera{})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "ALLIED.SAV")
			if err := s.WriteRetailSave(path, inputs); err != nil {
				t.Fatal(err)
			}
			for _, flag := range []bool{false, true} {
				loaded, err := LoadRetailSavePath(path, RetailLoadDeps{FS: fs, Catalog: cat, Gameplay: cfg.Gameplay, SimSeed: 17, CRTSeed: 19, UnitLimit: s.Skirmish.UnitLimit, AlliedSight: flag})
				if err != nil {
					t.Fatalf("load: %v", err)
				}
				restored := loaded.Battle.Session
				if got := sees(restored, 1); got != flag || restored.Skirmish.AlliedSight != flag {
					t.Fatalf("loaded with the flag %v: the human sees its teammate's commander %v (setup %v)", flag, got, restored.Skirmish.AlliedSight)
				}
				if sees(restored, 2) {
					t.Fatal("the load sees the opponent's commander")
				}
			}
		}
	}
}

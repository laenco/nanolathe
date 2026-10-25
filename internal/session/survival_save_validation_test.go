package session

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/ai"
	"github.com/nanolathe-gg/nanolathe/internal/survival"
	"github.com/nanolathe-gg/nanolathe/internal/world"
)

func survivalSaveValidationRecord() *SurvivalRecord {
	return &SurvivalRecord{
		Version:  SurvivalRecordVersion,
		Setup:    SurvivalSkirmishConfig("fixture", 1, SurvivalOptions{}),
		Attacker: 2, Team: []uint8{0, 1}, CentreX: 1, CentreZ: 1,
		Phase: uint8(survivalActive), Wave: 2, Survived: 1,
		Plan: SurvivalPlanRecord{Number: 2, Budget: 1, Groups: []SurvivalPlanGroupRec{
			{Domain: uint8(survival.Ground), Units: []string{"soldiera", "missing", "soldierb"}},
		}},
		Info: &SurvivalInfoRecord{Team: []uint8{0, 1}, Computer: []bool{false, true}, Starts: [][2]int32{{24, 24}, {40, 40}}},
	}
}

// Both entry points reject malformed director state before publishing it or
// mutating any session/world state (DESIGN_SURVIVAL §11).
func TestSurvivalSaveRejectsMalformedDirector(t *testing.T) {
	tests := []struct {
		name string
		edit func(*SurvivalRecord)
	}{
		{"version", func(r *SurvivalRecord) { r.Version++ }},
		{"invalid survivor", func(r *SurvivalRecord) { r.Team = []uint8{0, 10} }},
		{"duplicate survivor", func(r *SurvivalRecord) { r.Team = []uint8{0, 0} }},
		{"attacker in team", func(r *SurvivalRecord) { r.Team = []uint8{0, 2} }},
		{"attacker outside pool", func(r *SurvivalRecord) { r.Attacker = 10 }},
		{"setup player count", func(r *SurvivalRecord) { r.Setup.NumPlayers = 4 }},
		{"setup scenario disabled", func(r *SurvivalRecord) { r.Setup.Survival.Enabled = false }},
		{"attacker human", func(r *SurvivalRecord) { r.Setup.Players[2].Controller = SkirmishControllerHuman }},
		{"pace", func(r *SurvivalRecord) { r.Setup.Survival.Pace = 255 }},
		{"phase", func(r *SurvivalRecord) { r.Phase = 255 }},
		{"domain", func(r *SurvivalRecord) { r.Plan.Groups[0].Domain = uint8(survival.DomainCount) }},
		{"negative wave", func(r *SurvivalRecord) { r.Wave = -1 }},
		{"plan number", func(r *SurvivalRecord) { r.Plan.Number++ }},
		{"negative group", func(r *SurvivalRecord) { r.NextGroup = -1 }},
		{"negative pick", func(r *SurvivalRecord) { r.NextPick = -1 }},
		{"group past end", func(r *SurvivalRecord) { r.NextGroup = 2 }},
		{"pick past end", func(r *SurvivalRecord) { r.NextPick = 4 }},
		{"pick after completed plan", func(r *SurvivalRecord) { r.NextGroup, r.NextPick = 1, 1 }},
		{"downtime with pending picks", func(r *SurvivalRecord) { r.Phase, r.Survived = uint8(survivalDowntime), 2 }},
		{"warning with partial cursor", func(r *SurvivalRecord) { r.Phase, r.NextPick = uint8(survivalWarning), 1 }},
		{"grace with planned wave", func(r *SurvivalRecord) { r.Phase = uint8(survivalGrace) }},
		{"active survived count", func(r *SurvivalRecord) { r.Survived = r.Wave }},
		{"negative budget", func(r *SurvivalRecord) { r.Plan.Budget = -1 }},
		{"overflow budget", func(r *SurvivalRecord) { r.Plan.Budget = 1 << 62 }},
		{"deposit anchors", func(r *SurvivalRecord) { r.Deposits = 1 }},
		{"scenario lengths", func(r *SurvivalRecord) { r.Info.Computer = nil }},
		{"scenario survivor", func(r *SurvivalRecord) { r.Info.Team[1] = 10 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := survivalSaveValidationRecord()
			tc.edit(r)
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := SidecarSurvival(raw); err == nil || !strings.Contains(err.Error(), "save/sidecar/survival") {
				t.Fatalf("decode error %v, want a contextual refusal", err)
			}
			old := &survivalState{attacker: 9}
			s := &Session{Survival: old, EnemyOwner: 9, Skirmish: survivalSaveValidationRecord().Setup}
			s.AI[2] = &ai.Manager{}
			before := &Session{Survival: old, EnemyOwner: 9, Skirmish: s.Skirmish, AI: s.AI}
			if err := restoreSurvival(s, r); err == nil || !strings.Contains(err.Error(), "save/sidecar/survival") {
				t.Fatalf("direct restore error %v, want a contextual refusal", err)
			}
			if !reflect.DeepEqual(s, before) || s.AI[2].Passive {
				t.Fatal("refused record mutated the session")
			}
		})
	}
}

// A completed pick can remain at the end of its group until the following
// spawn tick; Warning retains the old wave's exhausted group cursor. These
// are ordinary between-tick saves, not corrupt indices (DESIGN_SURVIVAL §6).
func TestSurvivalSaveAcceptsDirectorCursorBoundaries(t *testing.T) {
	for _, phase := range []survivalPhase{survivalGrace, survivalWarning, survivalActive, survivalDowntime} {
		r := survivalSaveValidationRecord()
		r.Phase = uint8(phase)
		switch phase {
		case survivalGrace:
			r.Wave, r.Survived, r.Plan = 0, 0, SurvivalPlanRecord{}
		case survivalWarning:
			r.NextGroup = survival.DefaultTuning(survival.PaceNormal).MaxDirections
		case survivalActive:
			r.NextPick = len(r.Plan.Groups[0].Units)
		case survivalDowntime:
			r.NextGroup, r.Survived = len(r.Plan.Groups), r.Wave
		}
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := SidecarSurvival(raw); err != nil {
			t.Errorf("phase %d: %v", phase, err)
		}
	}
}

func TestSurvivalRestoreValidatesSavedWorldBeforeMutation(t *testing.T) {
	for _, tc := range []string{"player count", "map", "missing attacker manager", "start outside map", "deposit outside map"} {
		t.Run(tc, func(t *testing.T) {
			r := survivalSaveValidationRecord()
			s := &Session{Skirmish: r.Setup, World: &world.Terrain{CellW: 4, CellH: 4}}
			s.AI[2] = &ai.Manager{}
			switch tc {
			case "player count":
				s.Skirmish.NumPlayers++
			case "map":
				s.Skirmish.MapName = "another-map"
			case "missing attacker manager":
				s.AI[2] = nil
			case "start outside map":
				r.CentreX = 4
			case "deposit outside map":
				r.Deposits, r.DepositAt = 1, [][2]int32{{4, 1}}
			}
			before := &Session{Skirmish: s.Skirmish, World: s.World, AI: s.AI}
			if err := restoreSurvival(s, r); err == nil {
				t.Fatal("accepted a record inconsistent with the staged world")
			}
			if !reflect.DeepEqual(s, before) || (s.AI[2] != nil && s.AI[2].Passive) {
				t.Fatal("refused record mutated the session or world")
			}
		})
	}
}

// JSON drops the private defaults marker; Restart must still retain explicit
// zero-valued setup choices instead of applying registry defaults again.
func TestSurvivalRestorePreservesRestartSettings(t *testing.T) {
	r := survivalSaveValidationRecord()
	r.Setup.Difficulty, r.Setup.Location, r.Setup.CommanderDeath = 0, 0, 0
	r.Setup.Mapping, r.Setup.LineOfSight, r.Setup.LOSType = 0, 0, 0
	r.Setup.Players[1].Side, r.Setup.Players[1].Color, r.Setup.Players[1].AllyGroup = 0, 0, 0
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	r, err = SidecarSurvival(raw)
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{Skirmish: SkirmishConfig{MapName: "fixture", NumPlayers: 3, UnitLimit: 1000}, Catalog: survivalRestrictionCatalog(t)}
	s.AI[2] = &ai.Manager{}
	if err := restoreSurvival(s, r); err != nil {
		t.Fatal(err)
	}
	want := r.Setup
	want.rulesDefaultsApplied = true
	want.UnitLimit, want.Gameplay = 1000, s.Gameplay
	got := s.Skirmish
	got.ApplyDefaults()
	if err := got.Normalize(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restart config %+v, want saved choices %+v", got, want)
	}
}

// Missing content still drops its picks, including any already behind the
// saved cursor; validation must not turn that supported load into a refusal
// or leave the cursor pointing beyond the rebuilt group (DESIGN_SURVIVAL §11).
func TestSurvivalRestoreDropsMissingPicksBehindCursor(t *testing.T) {
	r := survivalSaveValidationRecord()
	r.NextPick = 2
	s := &Session{Skirmish: r.Setup, Catalog: survivalRestrictionCatalog(t)}
	s.AI[2] = &ai.Manager{}
	if err := restoreSurvival(s, r); err != nil {
		t.Fatal(err)
	}
	got := RecordSurvival(s)
	if got.NextGroup != 0 || got.NextPick != 1 || !reflect.DeepEqual(got.Plan.Groups[0].Units, []string{"soldiera", "soldierb"}) {
		t.Fatalf("rebuilt plan %+v at cursor (%d,%d)", got.Plan, got.NextGroup, got.NextPick)
	}
	if !s.AI[2].Passive {
		t.Fatal("attacker manager was not made passive")
	}
}

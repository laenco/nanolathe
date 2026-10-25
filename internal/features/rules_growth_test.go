package features

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/sim/rng"
)

// Modern's admission departure preserves the retail ordering and draw budgets
// below capacity; Strict/Community retain [05 R-FEAT-01 §3 step 4].
func TestModernFeatureGrowthAndSharedEventAdmission(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rules Rules
		grows bool
	}{
		{"unbound", nil, false}, {"strict", StrictRules{}, false},
		{"community", CommunityRules{}, false}, {"modern", ModernRules{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sim, crt := rng.NewSimulation(41), rng.NewCRT(43)
			s := NewService(newTestTerrainP1(64, 64), &sim, &crt, nil)
			s.Rules = tc.rules
			wreck := defP1("wreck", 1, 1, "wreck", "")
			wreck.Reclaimable = true
			wreck.Metal = 8
			for i := 0; i < FeatureAnimSlots; i++ {
				if s.PlaceAt(i%64, i/64, wreck) == nil {
					t.Fatalf("early refusal at %d", i)
				}
			}
			beforeSim, beforeCRT := sim.Draws(), crt.Draws()
			if got := s.PlaceAt(0, 32, wreck); (got != nil) != tc.grows {
				t.Fatalf("over-capacity placement=%v", got != nil)
			}
			// Replacement frees the predecessor's charge before admission, in every mode.
			if s.PlaceAt(0, 0, wreck) == nil {
				t.Fatal("replacement refused at capacity")
			}
			if sim.Draws() != beforeSim || crt.Draws() != beforeCRT {
				t.Fatal("feature admission consumed RNG")
			}
			tree := defP1("tree", 1, 1, "", "trees")
			tree.Flamable = true
			tree.SeqNameBurn = "burn"
			tree.SeqNameDie = "die"
			tree.SparkTime = 150
			stubSequences(s, longBurn(), []int32{100}, nil)
			if s.PlaceAt(2, 32, tree) == nil || s.PlaceAt(3, 32, tree) == nil {
				t.Fatal("resting sprites charged the arena")
			}
			if s.igniteAt(2, 32, tree) != tc.grows {
				t.Fatal("ignition did not use shared arena policy")
			}
			if got := sim.Draws() - beforeSim; got != boolDraw(tc.grows) || crt.Draws() != beforeCRT {
				t.Fatalf("ignition draws=%d", got)
			}
			if !s.transitionFeatureAt(3, 32, tree, false) {
				t.Fatal("transition must retain feature at this visit")
			}
			if s.InstanceAt(3, 32).IsAnimating != tc.grows {
				t.Fatal("event record did not use shared arena policy")
			}
			metal, energy, ok := s.ReclaimAt(0, 0)
			if !ok || metal != 8 || energy != float32(wreck.Energy) {
				t.Fatalf("reclaim=%v %v %v", metal, energy, ok)
			}
			if !tc.grows && s.PlaceAt(4, 32, wreck) == nil {
				t.Fatal("reclaim failed to release capacity")
			}
		})
	}
}

func boolDraw(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

func TestModernFeatureTerrainPopulationAndMapBound(t *testing.T) {
	for _, tc := range []struct {
		rules Rules
		want  int
	}{{StrictRules{}, FeatureAnimSlots}, {CommunityRules{}, FeatureAnimSlots}, {ModernRules{}, 4096}} {
		terrain := newTestTerrainP1(64, 64)
		def := defP1("wreck", 1, 1, "wreck", "")
		terrain.FeatureDefs = []*content.FeatureDef{def}
		for i := range terrain.Plot {
			terrain.Plot[i].SetFeature(0)
		}
		s := NewService(terrain, nil, nil, nil)
		s.Rules = tc.rules
		if got := s.PopulateFromTerrain(); got != tc.want {
			t.Fatalf("%T population=%d want %d", tc.rules, got, tc.want)
		}
		if s.PopulateFromTerrain() != 0 {
			t.Fatal("population was not idempotent")
		}
	}
	small := NewService(newTestTerrainP1(2, 2), nil, nil, nil)
	small.Rules = ModernRules{}
	if small.arenaLimit() != 4 {
		t.Fatal("Modern did not derive its bound from the map")
	}
	def := defP1("wreck", 1, 1, "wreck", "")
	for i := 0; i < 4; i++ {
		if small.PlaceAt(i%2, i/2, def) == nil {
			t.Fatal("map cell refused")
		}
	}
	if small.PlaceAt(2, 0, def) != nil || small.PlaceAt(0, 0, def) == nil {
		t.Fatal("map bounds or replacement changed")
	}
}

// Canonical capture identifies the selected policy and refuses unreviewed or
// typed-nil leaves before touching any simulation state.
func TestFeaturePoolCheckpointPolicyTags(t *testing.T) {
	for _, tc := range []struct {
		rules Rules
		want  uint8
	}{{nil, 0}, {StrictRules{}, 1}, {&StrictRules{}, 1}, {CommunityRules{}, 2}, {&CommunityRules{}, 2}, {ModernRules{}, 3}, {&ModernRules{}, 3}} {
		got, err := CheckpointRulesKind(tc.rules)
		if err != nil || got != tc.want {
			t.Fatalf("%T tag=%d error=%v", tc.rules, got, err)
		}
	}
	type unknown struct{ ModernRules }
	for _, rules := range []Rules{(*StrictRules)(nil), (*CommunityRules)(nil), (*ModernRules)(nil), unknown{}} {
		if _, err := CheckpointRulesKind(rules); err == nil {
			t.Fatalf("admitted %T", rules)
		}
	}
	s, c, _ := featureCheckpointFixture(t)
	before := featureCheckpointBytes(t, s, c)
	s.Rules = ModernRules{}
	after := featureCheckpointBytes(t, s, c)
	if string(before) == string(after) {
		t.Fatal("canonical payload omitted feature policy")
	}
}

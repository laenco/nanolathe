package orders

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// Nanolathe Modern policy, user-authorized 2026-10-22: DESIGN_UNITS_ORDERS_COB
// "Smarter patrol — 2026-10-22". The fixtures are authored; the builder stands
// at the origin on a leg from (0,0) to (600,0), so a candidate on that line
// has no detour.

func (f *modernWorkFixture) reset() {
	f.q.primary = []*Node{f.patrol, f.next, f.successor}
}

// modernClaimer is a healthy unit (never a candidate itself) running row id,
// aimed at target or at the goal (x, z).
func modernClaimer(h pool.Handle, owner uint8, id ID, target pool.Handle, x, z int32) *units.Unit {
	u := modernPatient(h, -50, 0, false)
	u.Health, u.Owner = 100, owner
	QueueForUnit(u).primary = []*Node{{ID: id, Owner: h, Target: target, GoalX: numeric.Fixed(int64(x) << 16), GoalZ: numeric.Fixed(int64(z) << 16)}}
	return u
}

func modernPatrolAirs(t *testing.T, run func(t *testing.T, f *modernWorkFixture)) {
	for _, air := range []bool{false, true} {
		t.Run(fmt.Sprintf("air%v", air), func(t *testing.T) {
			f := newModernWorkFixture(air)
			f.u.Def.SightDistance = 280 // a quarter of sight is 70, the leash 560
			run(t, f)
		})
	}
}

func TestModernPatrolScoreOrder(t *testing.T) {
	modernPatrolAirs(t, func(t *testing.T, f *modernWorkFixture) {
		random := f.sim
		// A near reclaim (detour 2 plus the reclaim offset 70) beats a far
		// construction (detour 228), which strict category order put first.
		f.resources.Stock[0] = 0
		f.units = []*units.Unit{modernPatient(2, 100, 250, true)}
		f.features = []FeatureView{modernFeature(3, 48, 16, 10, 0)}
		f.visit(100)
		if id := f.q.Head().ID; id != rowReclaim && id != rowVTOLReclaim {
			t.Fatal("far construction beat a near reclaim")
		}
		// The detour prefers work ahead over nearer work behind.
		f.reset()
		f.features = nil
		f.units = []*units.Unit{modernPatient(4, -100, 0, false), modernPatient(5, 120, 0, false)}
		f.visit(101)
		if f.q.Head().Target != 5 {
			t.Fatal("nearer work behind beat work ahead")
		}
		// Each friendly builder already on a target adds a quarter of sight.
		a, b := modernPatient(2, 100, 0, true), modernPatient(3, 150, 0, true)
		for _, tc := range []struct {
			name  string
			units []*units.Unit
			want  pool.Handle
		}{
			{"no helpers", []*units.Unit{a, b}, 2},
			{"one helper", []*units.Unit{a, b, modernClaimer(9, 0, rowHelpBuild, 2, 0, 0)}, 3},
			{"helpers on both", []*units.Unit{a, b, modernClaimer(9, 0, rowHelpBuild, 2, 0, 0), modernClaimer(10, 0, rowRepairUnit, 2, 0, 0), modernClaimer(11, 0, rowMobileBuild, 3, 0, 0)}, 3},
			{"hostile helper", []*units.Unit{a, b, modernClaimer(9, 1, rowHelpBuild, 2, 0, 0)}, 2},
			{"only job", []*units.Unit{a, modernClaimer(9, 0, rowHelpBuild, 2, 0, 0)}, 2},
		} {
			f.reset()
			f.units = tc.units
			f.visit(102)
			if got := f.q.Head().Target; got != tc.want {
				t.Fatalf("%s: picked %d, want %d", tc.name, got, tc.want)
			}
		}
		// Equal score and distance: unit work precedes reclaim.
		f.reset()
		f.units = []*units.Unit{modernPatient(2, 0, 100, true), modernClaimer(9, 0, rowHelpBuild, 2, 0, 0)}
		f.features = []FeatureView{modernFeature(1, 0, -96, 10, 0)}
		f.features[0].Z = numeric.Fixed(-100 << 16)
		f.visit(103)
		if f.q.Head().Target != 2 {
			t.Fatal("a tied reclaim beat unit work")
		}
		if f.sim != random {
			t.Fatal("scoring drew RNG")
		}
	})
}

func TestModernPatrolExclusiveReclaim(t *testing.T) {
	modernPatrolAirs(t, func(t *testing.T, f *modernWorkFixture) {
		f.resources.Stock[0] = 0
		near, far := modernFeature(1, 32, 0, 10, 0), modernFeature(2, 64, 0, 10, 0)
		for _, tc := range []struct {
			name     string
			claimer  *units.Unit
			features []FeatureView
			want     numeric.Fixed // 0: no job
		}{
			{"unclaimed", nil, []FeatureView{near, far}, near.X},
			{"reclaim claim", modernClaimer(9, 0, rowReclaim, 0, 32, 0), []FeatureView{near, far}, far.X},
			{"air reclaim claim", modernClaimer(9, 0, rowVTOLReclaim, 0, 32, 0), []FeatureView{near, far}, far.X},
			{"resurrection claim", modernClaimer(9, 0, rowResurrect, 0, 32, 0), []FeatureView{near, far}, far.X},
			{"hostile claim", modernClaimer(9, 1, rowReclaim, 0, 32, 0), []FeatureView{near, far}, near.X},
			{"only feature claimed", modernClaimer(9, 0, rowReclaim, 0, 32, 0), []FeatureView{near}, 0},
		} {
			f.reset()
			f.units = nil
			if tc.claimer != nil {
				f.units = []*units.Unit{tc.claimer}
			}
			f.features = tc.features
			f.visit(100)
			got := numeric.Fixed(0)
			if f.q.Head() != f.patrol {
				got = f.q.Head().GoalX
			}
			if got != tc.want {
				t.Fatalf("%s: reclaim goal %d, want %d", tc.name, got>>16, tc.want>>16)
			}
		}
	})
}

// The leash is inclusive, in whole world units, from the closed leg segment,
// both beside it and past its end; a patrol with no previous waypoint record
// has none.
func TestModernPatrolRouteLeashBoundary(t *testing.T) {
	modernPatrolAirs(t, func(t *testing.T, f *modernWorkFixture) {
		for _, tc := range []struct {
			name   string
			bx, bz int32
			x, z   int32
			raw    numeric.Fixed
			leg    bool
			want   bool
		}{
			{"beside, on the leash", 300, 600, 300, 560, 0, true, true},
			{"beside, fraction on the leash", 300, 600, 300, 560, 0xffff, true, true},
			{"beside, past the leash", 300, 600, 300, 561, 0, true, false},
			{"past the end, inside", 900, 300, 995, 395, 0, true, true},
			{"past the end, outside", 900, 300, 996, 396, 0, true, false},
			{"no previous waypoint", 300, 600, 300, 561, 0, false, true},
		} {
			f.reset()
			if !tc.leg {
				f.q.primary = []*Node{f.patrol, f.successor}
			}
			f.u.X, f.u.Z = numeric.FixedFromInt(int64(tc.bx)), numeric.FixedFromInt(int64(tc.bz))
			target := modernPatient(2, tc.x, tc.z, false)
			target.Z += tc.raw
			f.units = []*units.Unit{target}
			f.visit(100)
			if got := f.q.Head() != f.patrol; got != tc.want {
				t.Fatalf("%s: selected=%v want %v", tc.name, got, tc.want)
			}
		}
	})
}

func TestModernPatrolMemoryRing(t *testing.T) {
	var s patrolWorkState
	s.scanDue = 40
	for i := int32(1); i <= modernPatrolMemorySize+1; i++ {
		s.remember(patrolMemoryEntry{kind: patrolMemoryFeature, cx: i})
	}
	if s.remembersFeature(1, 0) || s.entries[0].cx != modernPatrolMemorySize+1 || s.next != 1 {
		t.Fatalf("a full memory did not overwrite its oldest entry: %+v", s)
	}
	for i := int32(2); i <= modernPatrolMemorySize+1; i++ {
		if !s.remembersFeature(i, 0) {
			t.Fatalf("entry %d lost", i)
		}
	}
	held := s
	s.remember(patrolMemoryEntry{kind: patrolMemoryFeature, cx: 5})
	s.remember(patrolMemoryEntry{})
	if s != held {
		t.Fatal("a repeated or empty entry took a slot")
	}
	s.forget()
	if s != (patrolWorkState{scanDue: 40}) {
		t.Fatal("forget kept an entry or moved the cadence")
	}
	// Selection skips each remembered target kind.
	modernPatrolAirs(t, func(t *testing.T, f *modernWorkFixture) {
		f.resources.Stock[0] = 0
		patient := modernPatient(2, 32, 0, false)
		f.units = []*units.Unit{patient, modernPatient(3, 200, 0, false)}
		f.features = []FeatureView{modernFeature(4, 16, 0, 10, 0)}
		f.q.patrolWork.remember(patrolMemoryEntry{kind: patrolMemoryUnit, unit: patient, handle: patient.Handle})
		f.q.patrolWork.remember(patrolMemoryEntry{kind: patrolMemoryFeature, cx: 1})
		f.visit(100)
		if f.q.Head().Target != 3 {
			t.Fatal("selection did not skip the remembered unit and feature")
		}
		// A reused handle is a new unit: identity, not the handle, is remembered.
		f.reset()
		f.units = []*units.Unit{modernPatient(2, 32, 0, false)}
		f.visit(101)
		if f.q.Head().Target != 2 {
			t.Fatal("a reused handle inherited another unit's memory")
		}
	})
}

func TestModernPatrolFactoryMemory(t *testing.T) {
	modernPatrolAirs(t, func(t *testing.T, f *modernWorkFixture) {
		factory, other := modernPatient(10, 300, 0, false), modernPatient(11, 400, 0, false)
		for _, u := range []*units.Unit{factory, other} {
			u.Health, u.Def.Builder = 100, true
		}
		carried := func(h pool.Handle, x int32, by *units.Unit) *units.Unit {
			p := modernPatient(h, x, 0, true)
			p.Attachment.Carrier = by.Handle
			return p
		}
		first := carried(7, 100, factory)
		f.units = []*units.Unit{factory, first}
		f.visit(100)
		work := f.q.Head()
		if work.Target != first.Handle || work.workFactory != factory || work.WorkFactory() != factory.Handle {
			t.Fatal("product job did not record its factory")
		}
		// The completion transition detaches the finished product before the
		// job reports success [04 R-FAC-02 §3].
		first.Remaining, first.Health, first.Attachment.Carrier = 0, 100, 0
		// A product finished before this helper reached its work phase reports
		// the approach's "nothing left to assist" code 5: neutral, so neither
		// the factory nor the product is remembered and the leg resumes on the
		// next tick.
		approach, worked := uint8(1), uint8(4) // ground HelpBuild
		if f.u.Def.CanFly {
			approach, worked = 2, 3 // VTOL_HelpBuild has no phase 4
		}
		work.Phase = approach
		if code := f.q.binding.Rules.AutomaticWorkResult(f.u, work, 0, 5, 101); code != 8 || f.q.patrolWork.remembers(patrolMemoryFactory, factory) || f.q.patrolWork.remembers(patrolMemoryUnit, first) {
			t.Fatal("a product finished before the helper worked remembered its factory or itself")
		}
		if f.patrol.DynamicGate != gateDeadline || f.patrol.Deadline != 102 {
			t.Fatal("a neutral early finish did not resume the leg on the next tick")
		}
		first = carried(7, 100, factory)
		f.reset()
		f.units = []*units.Unit{factory, first}
		f.visit(100)
		if work = f.q.Head(); work.Target != first.Handle {
			t.Fatal("the factory's product was not eligible after a neutral early finish")
		}
		first.Remaining, first.Health, first.Attachment.Carrier = 0, 100, 0
		work.Phase = worked
		if code := f.q.binding.Rules.AutomaticWorkResult(f.u, work, 0, 5, 101); code != 5 || !f.q.patrolWork.remembers(patrolMemoryFactory, factory) {
			t.Fatal("successful product assist did not remember its factory")
		}
		next, elsewhere := carried(8, 100, factory), carried(9, 150, other)
		f.reset()
		f.units = []*units.Unit{factory, next, other, elsewhere}
		f.visit(102)
		if f.q.Head().Target != elsewhere.Handle {
			t.Fatal("remembered factory's next product was assisted, or another factory's was not")
		}
		f.reset()
		f.units = []*units.Unit{factory, next}
		f.visit(103)
		if f.q.Head() != f.patrol {
			t.Fatal("remembered factory was assisted before a waypoint")
		}
		f.q.binding.Rules.AutomaticWorkResult(f.u, f.patrol, gateArrived, 6, 104)
		f.visit(105)
		work = f.q.Head()
		if work.Target != next.Handle {
			t.Fatal("waypoint arrival did not release the factory")
		}
		// A failed assist remembers the product, not its factory.
		if code := f.q.binding.Rules.AutomaticWorkResult(f.u, work, 0, 8, 106); code != 8 || !f.q.patrolWork.remembers(patrolMemoryUnit, next) || f.q.patrolWork.remembers(patrolMemoryFactory, factory) {
			t.Fatal("failed product assist remembered the wrong target")
		}
	})
}

// Energy below a fifth of storage refuses repair alone; construction
// assistance and reclaim need none.
func TestModernPatrolEnergyGateSplit(t *testing.T) {
	modernPatrolAirs(t, func(t *testing.T, f *modernWorkFixture) {
		f.resources.Stock = [2]float32{0, 19}
		random := f.sim
		for _, tc := range []struct {
			name     string
			units    []*units.Unit
			features []FeatureView
			energy   float32
			want     bool
		}{
			{"construction", []*units.Unit{modernPatient(2, 32, 0, true)}, nil, 19, true},
			{"repair", []*units.Unit{modernPatient(2, 32, 0, false)}, nil, 19, false},
			{"reclaim", nil, []FeatureView{modernFeature(3, 32, 0, 10, 0)}, 19, true},
			{"repair with energy", []*units.Unit{modernPatient(2, 32, 0, false)}, nil, 21, true},
		} {
			f.reset()
			f.units, f.features, f.resources.Stock[1] = tc.units, tc.features, tc.energy
			f.visit(100)
			if got := f.q.Head() != f.patrol; got != tc.want {
				t.Fatalf("%s: selected=%v want %v", tc.name, got, tc.want)
			}
		}
		if f.sim != random {
			t.Fatal("energy gate drew RNG")
		}
	})
}

// A moving patient is skipped unless the builder already reaches it; the
// mover's scalar speed word says whether it moves. Construction is unaffected.
func TestModernPatrolSkipsMovingRepairTargets(t *testing.T) {
	modernPatrolAirs(t, func(t *testing.T, f *modernWorkFixture) {
		f.u.Def.BuildDistance = 40 // with both 2×2 footprints, reach ends at 84 units
		for _, tc := range []struct {
			name       string
			x          int32
			unfinished bool
			speed      numeric.Fixed
			want       bool
		}{
			{"moving beyond reach", 85, false, numeric.FixedFromInt(1), false},
			{"moving within reach", 84, false, numeric.FixedFromInt(1), true},
			{"stationary beyond reach", 85, false, 0, true},
			{"moving construction", 200, true, numeric.FixedFromInt(1), true},
		} {
			f.reset()
			target := modernPatient(2, tc.x, 0, tc.unfinished)
			target.Move.Speed = tc.speed
			f.units = []*units.Unit{target}
			f.visit(100)
			if got := f.q.Head() != f.patrol; got != tc.want {
				t.Fatalf("%s: selected=%v want %v", tc.name, got, tc.want)
			}
		}
	})
}

// An animating feature is never selected, and a reclaim whose feature
// disappears or is replaced is neutral: no memory, no return, the leg
// resumes on the next tick. A reclaim failing on a resting feature is a
// failure and remembers its anchor.
func TestModernPatrolDyingFeature(t *testing.T) {
	modernPatrolAirs(t, func(t *testing.T, f *modernWorkFixture) {
		f.resources.Stock[0] = 0
		dying := modernFeature(1, 32, 0, 10, 0)
		dying.Animating = true
		f.features = []FeatureView{dying, modernFeature(2, 64, 0, 10, 0)}
		f.visit(100)
		if f.q.Head().GoalX != numeric.FixedFromInt(64) {
			t.Fatal("selected an animating feature")
		}
	})
	for _, outcome := range []string{"gone", "successor", "animating", "resting"} {
		t.Run(outcome, func(t *testing.T) {
			modernPatrolAirs(t, func(t *testing.T, f *modernWorkFixture) {
				f.resources.Stock[0] = 0
				f.features = []FeatureView{modernFeature(1, 32, 0, 10, 0)}
				f.visit(100)
				work := f.q.Head()
				switch outcome {
				case "gone":
					f.features = nil
				case "successor":
					f.features[0].Reclaimable, f.features[0].Metal = false, 0
				case "animating":
					f.features[0].Animating = true
				}
				code := f.q.binding.Rules.AutomaticWorkResult(f.u, work, 0, 8, 101)
				f.q.unlinkPrimary(work)
				remembered := f.q.patrolWork.remembersFeature(2, 0)
				if code != 8 || remembered != (outcome == "resting") || f.q.Head() != f.patrol || len(f.q.primary) != 3 {
					t.Fatalf("code=%d remembered=%v head=%v rows=%d", code, remembered, f.q.Head() == f.patrol, len(f.q.primary))
				}
				if f.patrol.DynamicGate != gateDeadline || f.patrol.Deadline != 102 {
					t.Fatal("the leg did not resume on the next tick")
				}
			})
		})
	}
}

// Selection runs every 15 ticks between maintenance visits without
// re-arming the leg's 60-tick ground or 45-tick air maintenance. Strict 3.1
// and Community never wake.
func TestModernPatrolScanCadence(t *testing.T) {
	for _, air := range []bool{false, true} {
		for _, rules := range []Rules{&ModernRules{}, StrictRules{}, &CommunityRules{}} {
			t.Run(fmt.Sprintf("air%v/%T", air, rules), func(t *testing.T) {
				f := newModernWorkFixture(air)
				f.q.binding.Rules = rules
				_, modern := rules.(*ModernRules)
				id := rowHelpBuild
				if air {
					id = rowVTOLHelpBuild
				}
				f.q.SetOwnedHandler(id, func(_ *units.Unit, n *Node, _ uint32, tick uint32) (Code, bool) {
					n.DynamicGate, n.Deadline = gateDeadline, int32(tick+1000)
					return 2, true
				})
				f.q.Pump(f.u, 100)
				deadline := int32(160)
				if air {
					deadline = 145
				}
				if f.q.Head() != f.patrol || f.patrol.Deadline != deadline {
					t.Fatalf("maintenance deadline %d, want %d", f.patrol.Deadline, deadline)
				}
				scans, random, resources := f.scans, f.sim, f.resources
				f.units = []*units.Unit{modernPatient(2, 32, 0, true)}
				for tick := uint32(101); tick < 115; tick++ {
					f.q.Pump(f.u, tick)
					if f.q.Head() != f.patrol || f.scans != scans || f.patrol.Deadline != deadline {
						t.Fatalf("tick %d selected or re-armed the leg before the cadence", tick)
					}
				}
				f.q.Pump(f.u, 115)
				if !modern {
					if f.q.Head() != f.patrol || f.scans != scans || f.sim != random || f.resources != resources || f.q.patrolWork != (patrolWorkState{}) {
						t.Fatal("Strict/Community woke a blocked patrol")
					}
					return
				}
				if f.q.Head().ID != id || f.q.Head().workAssignment != f.patrol || f.patrol.Deadline != deadline || f.q.patrolWork.scanDue != 130 || f.sim != random || f.resources != resources {
					t.Fatal("the 15-tick wake did not borrow without touching maintenance, RNG or resources")
				}
				// No wake without an installed leg, off phase 1, or off the head.
				f.reset()
				f.patrol.DynamicGate = gateDeadline
				if rules.PatrolWorkWake(f.u, f.patrol, 500) {
					t.Fatal("woke a patrol whose leg is not installed")
				}
				f.patrol.DynamicGate, f.patrol.Phase = gateDeadline|gateMoveOutcomes, 0
				if rules.PatrolWorkWake(f.u, f.patrol, 500) {
					t.Fatal("woke a patrol outside phase 1")
				}
				f.patrol.Phase = 1
				if rules.PatrolWorkWake(f.u, f.next, 500) {
					t.Fatal("woke a record that is not the head")
				}
			})
		}
	}
}

// Strict 3.1 and Community answer every patrol hook the retail way and touch
// no Modern state, RNG or resources.
func TestModernPatrolStrictCommunityBypass(t *testing.T) {
	for _, rules := range []Rules{StrictRules{}, &CommunityRules{}} {
		for _, air := range []bool{false, true} {
			t.Run(fmt.Sprintf("%T/air%v", rules, air), func(t *testing.T) {
				f := newModernWorkFixture(air)
				f.units = []*units.Unit{modernPatient(2, 32, 0, true)}
				f.visit(100) // Modern borrows, so the bypass sees real receipts
				work := f.q.Head()
				f.q.binding.Rules = rules
				before := append([]*Node(nil), f.q.primary...)
				random, resources, memory := f.sim, f.resources, f.q.patrolWork
				for code := Code(0); code <= 9; code++ {
					for _, n := range []*Node{work, f.q.primary[1], f.patrol} {
						if got := rules.AutomaticWorkResult(f.u, n, gateArrived, code, 101); got != code {
							t.Fatalf("code %d became %d", code, got)
						}
					}
				}
				if _, handled := rules.PatrolWorkVisit(f.u, f.patrol, 102); handled || rules.PatrolWorkWake(f.u, f.patrol, 102) || !rules.AutomaticWorkValid(f.u, work) {
					t.Fatal("bypass rules handled a Modern patrol hook")
				}
				if len(f.q.primary) != len(before) || f.sim != random || f.resources != resources || f.q.patrolWork != memory {
					t.Fatal("bypass rules changed the queue, RNG, resources or memory")
				}
				for i := range before {
					if f.q.primary[i] != before[i] {
						t.Fatal("bypass rules reordered the queue")
					}
				}
			})
		}
	}
}

// Canonical capture keeps the memory's slot order, its cursor and cadence and
// each remembered unit's identity; a remembered unit is discovered, never
// written undiscovered.
func TestConstructorCheckpointPatrolMemory(t *testing.T) {
	// Both candidate units are roots, so each owns a fixed allocation identity.
	graph := func(mutate func(q *Queue, a, b *units.Unit)) checkpoint.Digests {
		q := &Queue{primary: []*Node{{ID: rowRepairPatrol}}}
		a, b := &units.Unit{Handle: 5}, &units.Unit{Handle: 6}
		mutate(q, a, b)
		return orderCheckpointDigests(t, &units.Unit{Orders: q}, a, b)
	}
	unitEntry := func(u *units.Unit) patrolMemoryEntry {
		return patrolMemoryEntry{kind: patrolMemoryUnit, unit: u, handle: u.Handle}
	}
	feature := patrolMemoryEntry{kind: patrolMemoryFeature, cx: 3, cz: 4}
	base := graph(func(q *Queue, a, _ *units.Unit) {
		q.patrolWork.remember(unitEntry(a))
		q.patrolWork.remember(feature)
	})
	for _, tc := range []struct {
		name   string
		mutate func(q *Queue, a, b *units.Unit)
	}{
		{"slot order", func(q *Queue, a, _ *units.Unit) {
			q.patrolWork.remember(feature)
			q.patrolWork.remember(unitEntry(a))
		}},
		{"unit identity", func(q *Queue, _, b *units.Unit) {
			q.patrolWork.remember(patrolMemoryEntry{kind: patrolMemoryUnit, unit: b, handle: 5})
			q.patrolWork.remember(feature)
		}},
		{"factory kind", func(q *Queue, a, _ *units.Unit) {
			q.patrolWork.remember(patrolMemoryEntry{kind: patrolMemoryFactory, unit: a, handle: a.Handle})
			q.patrolWork.remember(feature)
		}},
	} {
		if got := graph(tc.mutate); got.Full == base.Full {
			t.Fatalf("%s did not change the capture", tc.name)
		}
	}
	if got := graph(func(q *Queue, a, _ *units.Unit) {
		q.patrolWork.remember(unitEntry(a))
		q.patrolWork.remember(feature)
	}); got != base {
		t.Fatal("equal memories captured differently")
	}
	// A retired remembered unit is discovered by collection.
	retired := &units.Unit{Handle: 7}
	q := &Queue{primary: []*Node{{ID: rowRepairPatrol}}}
	q.patrolWork.remember(unitEntry(retired))
	c := orderCheckpointContext(t, &units.Unit{Orders: q})
	collectOrdersCheckpoint(t, c)
	if _, known := c.Units.Allocations.Find(retired); !known {
		t.Fatal("collection did not discover the remembered unit")
	}
	// An entry added after collection fails at its own field.
	q.patrolWork.remember(unitEntry(&units.Unit{Handle: 8}))
	var out bytes.Buffer
	err := (&Pump{}).WriteCheckpoint(checkpoint.NewEncoder(&out), c)
	if err == nil || !strings.Contains(err.Error(), "orders.queues[0].patrolWork.entries[1].unit") || !strings.Contains(err.Error(), "undiscovered reference") {
		t.Fatalf("undiscovered memory unit error = %v", err)
	}
}

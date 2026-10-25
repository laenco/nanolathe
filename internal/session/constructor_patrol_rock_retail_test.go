//go:build retail

package session

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/features"
	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
)

// A stock map rock keeps its reclaimable definition through its reclaim
// sequence and is then replaced by a non-reclaimable scar. Modern patrol
// used to re-pick the dying rock, fail on the scar and stop borrowing for the
// rest of the leg. DESIGN_UNITS_ORDERS_COB "Smarter patrol — 2026-10-22": an
// animating feature is never selected and a vanished one is neutral, so an
// ARMCK patrolling past the fixture map's rocks keeps reclaiming them.
func TestModernConstructorPatrolMapRockReclaimKeepsWorkingRetail(t *testing.T) {
	fixture := loadRetailFixture(t)
	fixture.cfg.Gameplay = gameplay.Modern
	s, err := NewSkirmishWithEntryOptions(fixture.fs, fixture.cat, fixture.cfg, SkirmishEntryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateComposition(); err != nil {
		t.Fatal(err)
	}
	driver := int32(0)
	step := func() {
		p := &s.Econ.Players[0]
		p.Capacity, p.Stock = [2]float32{10000, 10000}, [2]float32{5000, 5000}
		driver++
		s.Step(driver)
		if s.State != StateBattle {
			t.Fatalf("battle ended at tick %d", s.Clock.GlobalTick)
		}
	}
	for i := 0; i < 5; i++ {
		step()
	}
	at := numeric.FixedFromInt
	builder := placeCompleteRetailUnit(t, s, "armck", 0, at(1500), at(700))
	s.Movement.EnsureUnit(builder)
	s.bindOrderQueue(builder)
	q := orders.QueueForUnit(builder)
	q.CancelAll()
	if err := s.EnqueueHumanCommand(HumanCommand{Kind: HumanOrder, Order: HumanOrderCommand{
		Handles: []pool.Handle{builder.Handle}, Code: 9, AssignedPosition: true,
		Position: orders.ResolvePos{X: at(2600), Y: s.World.HeightAt(at(2600), at(700)), Z: at(700), InterfaceType: orders.InterfaceTypeRightClick},
	}}); err != nil {
		t.Fatal(err)
	}
	payouts := 0
	original := q.Binding().ReclaimFeatureHook()
	q.Binding().SetReclaimFeature(func(cx, cz int) (metal, energy float32, ok bool) {
		metal, energy, ok = original(cx, cz)
		if ok {
			payouts++
		}
		return
	})
	reclaim := orders.Lookup("Reclaim")
	var last *orders.Node
	paidAt, idle := uint32(0), uint32(0)
	for i := 0; i < 1500 && payouts < 4; i++ {
		before := payouts
		step()
		if payouts > before {
			paidAt = s.Clock.GlobalTick
		}
		head := q.Head()
		if head != nil && head.IsPatrolReturn() {
			t.Fatal("a reclaim end started a return trip")
		}
		if head != nil && head.ID == reclaim && head != last {
			// A newly borrowed reclaim never aims at a feature still playing its
			// sequence, so it cannot end on that feature's successor.
			_, cx, cz, ok := features.FeatureAt(s.World, head.GoalX, head.GoalZ)
			if !ok {
				t.Fatalf("borrowed a reclaim with no feature at (%d,%d)", head.GoalX.Floor(), head.GoalZ.Floor())
			}
			if inst := s.Features.InstanceAt(cx, cz); inst == nil || inst.IsAnimating || inst.IsBurning {
				t.Fatalf("borrowed an animating feature at cell (%d,%d)", cx, cz)
			}
			if paidAt != 0 && s.Clock.GlobalTick-paidAt > idle {
				idle = s.Clock.GlobalTick - paidAt
			}
			paidAt = 0
		}
		last = head
	}
	if payouts < 4 {
		t.Fatalf("only %d rock reclaims paid; head=%s pos=(%d,%d) memory=%+v", payouts, orders.DescriptorFor(q.Head().ID).Name, builder.X.Floor(), builder.Z.Floor(), q.PatrolWorkTrace())
	}
	// The next job follows a payout within one selection cadence.
	if idle > 15 {
		t.Fatalf("the builder waited %d ticks after a reclaim before its next job", idle)
	}
	for _, e := range q.PatrolWorkTrace().Entries {
		if e.Kind != 0 {
			t.Fatalf("a rock reclaim left a memory entry: %+v", e)
		}
	}
}

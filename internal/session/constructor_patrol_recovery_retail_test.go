//go:build retail

package session

import (
	"fmt"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// constructorPatrolRemembered reports whether the helper's failed-target
// memory holds a unit entry for h.
func (f *constructorPatrolRuntime) constructorPatrolRemembered(kind uint8, h uint32) bool {
	for _, e := range f.q.PatrolWorkTrace().Entries {
		if e.Kind == kind && uint32(e.Handle) == h {
			return true
		}
	}
	return false
}

func (f *constructorPatrolRuntime) constructorPatrolMemoryEmpty() bool {
	return f.q.PatrolWorkTrace().Entries == [8]orders.PatrolMemoryEntry{}
}

// Target loss is an ordinary failed job under DESIGN_UNITS_ORDERS_COB
// "Smarter patrol — 2026-10-22": the saved return is dropped, the helper
// continues the pending leg from where it stands and borrows other work at
// once. A destroyed target's reference is unlinked, so nothing is remembered
// against its reused handle. TestModernConstructorPatrolMovingTargetAbandonsRetail
// covers a remembered live target and its waypoint recovery.
func TestModernConstructorPatrolTargetLossContinuesLegRetail(t *testing.T) {
	for _, key := range []string{"armcv", "armca"} {
		for stance := uint32(0); stance < 3; stance++ {
			t.Run(fmt.Sprintf("%s/stance%d", key, stance), func(t *testing.T) {
				f := newConstructorPatrolRuntime(t, key, stance, nil)
				target := f.lateNanoframe(160)
				f.waitForBorrow(target)
				f.s.Units.Destroy(target.Handle, units.DeathUnknown)
				resumed := false
				for i := 0; i < 120 && !resumed; i++ {
					f.step()
					f.assertRetained()
					resumed = constructorPatrolName(f.q.Head()) == f.patrol
				}
				if !resumed {
					t.Fatalf("target destruction did not resume the patrol: helper alive=%v hp=%d target alive=%v dying=%v head=%s", f.helper.Alive, f.helper.Health, target.Alive, target.Dying, constructorPatrolName(f.q.Head()))
				}
				for _, n := range f.q.Primary() {
					if n.IsPatrolReturn() {
						t.Fatal("target loss kept the saved-position return")
					}
				}
				if !f.constructorPatrolMemoryEmpty() {
					t.Fatalf("a freed target left a memory entry: %+v", f.q.PatrolWorkTrace())
				}
				// Fresh work in sight is borrowed at once, even on the freed
				// target's reused handle: there is no route-wide pause.
				resumedAt := constructorPatrolPosition(f.helper)
				decoy := f.lateNanoframe(120)
				f.waitForBorrow(decoy)
				if resumedAt.distanceSquared(constructorPatrolPosition(f.helper)) > int64(f.helper.Def.SightDistance)*int64(f.helper.Def.SightDistance) {
					t.Fatal("the helper travelled away before borrowing fresh work")
				}
				for i := 0; i < 600; i++ {
					if head := f.q.Head(); head != nil && head.IsPatrolReturn() {
						t.Fatal("a job end started a return trip")
					}
					f.step()
					f.assertRetained()
				}
			})
		}
	}
}

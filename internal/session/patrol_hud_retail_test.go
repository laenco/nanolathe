//go:build retail

package session

import (
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"testing"
)

func TestPublishedPatrolWorkBorrowedAndReturnRetail(t *testing.T) {
	for _, key := range []string{"armcv", "armca"} {
		t.Run(key, func(t *testing.T) {
			f := newConstructorPatrolRuntime(t, key, 1, nil)
			target := f.lateNanoframe(160)
			f.observeWork(target)
			seenWork, seenResume := false, false
			for i := 0; i < 1800; i++ {
				f.step()
				head := f.q.Head()
				if head == nil {
					t.Fatal("patrol disappeared")
				}
				ordinal := f.q.WorkAssignmentOrdinal(head)
				name := orders.DescriptorFor(head.ID).Name
				if ordinal > 0 {
					if head.IsPatrolReturn() {
						// Every job end now continues the pending leg from where the
						// helper stands (DESIGN_UNITS_ORDERS_COB "Smarter patrol —
						// 2026-10-22"); the saved position never runs as a move.
						t.Fatal("a failed job started a saved-position return")
					}
					seenWork = true
					if allocations := testing.AllocsPerRun(100, func() { f.s.publishedPatrolWork(f.helper) }); allocations != 0 {
						t.Fatalf("borrowed patrol publication allocated %.1f times", allocations)
					}
					f.s.Units.Destroy(target.Handle, units.DeathUnknown)
				} else if seenWork && name == f.patrol {
					seenResume = true
				}
				if got := f.s.publishedPatrolWork(f.helper); got != 2 {
					t.Fatalf("%s label=%d want Both", name, got)
				}
				if seenWork && seenResume {
					break
				}
			}
			if !seenWork || !seenResume {
				t.Fatalf("missing work/resume: %t/%t", seenWork, seenResume)
			}
		})
	}
}

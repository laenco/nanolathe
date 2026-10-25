package utility

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/aikit"
	"github.com/nanolathe-gg/nanolathe/internal/content"
)

// A damaged own building is the next fallback after a producing factory:
// two idle constructors repair it with one group command instead of standing
// beside it. The commander stays out of the group.
func TestIdleBuildersRepairDamagedBuilding(t *testing.T) {
	w := newMetalWorld(DefaultParams(), aikit.PersonaEasy)
	w.d.con.Def = &content.UnitDef{CanReclamate: true}
	base := w.metalBase()
	base[3].HP = 10 // the home extractor
	base = append(base, own(6, w.d.con, 720, 720), own(7, w.d.con, 740, 700))
	w.thinkWith(30*60, base, metalAmpleRes, energyFullRes)
	if !w.e.rescueRepair(w.b) {
		t.Fatal("damaged building was not picked up by the idle-builder fallback")
	}
	var repaired int
	for i := range w.obs.Own {
		u := &w.obs.Own[i]
		c := w.e.s.commitOf(u)
		if c.kind == cAssist && c.target == 4 {
			repaired++
		}
	}
	if repaired < 2 {
		t.Fatalf("repair commitments = %d, want the two idle constructors", repaired)
	}
}

// The repair fallback waits for the long game: until minute 20 its group
// action competes with the opening economy's only builder action.
func TestIdleRepairFallbackStartsAfterOpening(t *testing.T) {
	w := newMetalWorld(DefaultParams(), aikit.PersonaEasy)
	w.d.con.Def = &content.UnitDef{CanReclamate: true}
	base := w.metalBase()
	base[3].HP = 10
	base = append(base, own(6, w.d.con, 720, 720), own(7, w.d.con, 740, 700))
	w.thinkWith(10*1800, base, metalAmpleRes, energyFullRes)
	if w.e.rescueIdle(w.b) {
		t.Fatal("repair fallback acted before the long game")
	}
	w.thinkWith(21*1800, base, metalAmpleRes, energyFullRes)
	if !w.e.rescueIdle(w.b) {
		t.Fatal("repair fallback did not act after the opening")
	}
}

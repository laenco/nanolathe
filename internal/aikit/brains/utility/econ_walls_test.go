package utility

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/aikit"
	"github.com/nanolathe-gg/nanolathe/internal/content"
)

// One constructor, never the commander, goes to a wall the army is held at:
// the nearest of the cheapest, only while more than breachKeep others
// remain, one at a time; it is called off once the army has stopped asking.
func TestBreachSendsOneConstructor(t *testing.T) {
	const tick = 20 * 1800
	setup := func(extra int) (*defWorld, []aikit.OwnUnit) {
		w := newDefWorld(DefaultParams())
		w.d.con.Def = &content.UnitDef{CanReclamate: true}
		w.d.com.Def = &content.UnitDef{CanReclamate: true}
		units := append(w.base(), own(20, w.d.con, 1200, 1200))
		if extra > 0 {
			units = append(units, own(21, w.d.con, 900, 900))
		}
		w.think(tick, units)
		w.b.BreachX, w.b.BreachZ, w.b.BreachTick = 2000, 2000, tick
		return w, units
	}
	w, _ := setup(0)
	if used := w.e.breach(w.b, 5); used != 0 {
		t.Fatalf("with two constructors one went (%d actions)", used)
	}
	w, units := setup(1)
	if used := w.e.breach(w.b, 5); used != 1 {
		t.Fatalf("with three constructors none went")
	}
	sent := &w.obs.Own[len(units)-2] // handle 20, the nearest
	if c := w.e.s.commitOf(sent); c.kind != cBreach {
		t.Fatalf("handle %d's commitment is %s, want breach", sent.H, commitNames[c.kind])
	}
	// Out and working: no second one; once the army stops asking, called off.
	sent.Order = aikit.OrderBuild
	w.b.BreachTick = tick
	if used := w.e.breach(w.b, 5); used != 0 {
		t.Fatalf("a second constructor went while the first works")
	}
	w.b.BreachTick = tick - breachLapse - 1
	if used := w.e.breach(w.b, 5); used != 1 || w.e.s.commitOf(sent).kind != cNone {
		t.Fatalf("the constructor was not called off (%d actions, %s)", used, commitNames[w.e.s.commitOf(sent).kind])
	}
}

// A segment stands wallAhead in front of a tower in an approached zone,
// its pieces touching across the approach, built by a free constructor,
// on later thinks too when one think's budget cannot order it all.
func TestWallSegmentCrossesTheApproach(t *testing.T) {
	w := newDefWorld(DefaultParams())
	w.d.con.Builds = append(w.d.con.Builds, w.d.drag)
	w.e.s.info[w.d.drag.Index].wall = true
	tick := uint32(w.e.s.p.WallFrom)*1800 + 10
	units := append(w.base(), own(30, w.d.llt, 1500, 1200))
	w.think(tick, units)
	w.b.Defenses = []int32{int32(len(units) - 1)}
	pl := w.e.plan()
	pl.refresh(w.e.s, w.b)
	pl.atkW[pl.zoneOf(1500, 1200)] = wallSeen
	if used := w.e.walls(w.b, 1); used != 1 {
		t.Fatalf("a one-action think ordered %d pieces", used)
	}
	if used := w.e.walls(w.b, 5); used != wallPieces-1 {
		t.Fatalf("the next think ordered %d more pieces, want %d", used, wallPieces-1)
	}
	dx, dz := pl.approach(w.e.s, 1500, 1200, pl.zoneOf(1500, 1200), dcGround)
	pts := w.e.wl.pts[:w.e.wl.n]
	if len(pts) != wallPieces {
		t.Fatalf("%d pieces planned", len(pts))
	}
	for i, p := range pts {
		ahead := (int64(p[0]-1500)*dx + int64(p[1]-1200)*dz) / one
		if ahead < wallAhead-24 || ahead > wallAhead+24 {
			t.Errorf("piece %d at (%d,%d) stands %d ahead of the tower, want about %d", i, p[0], p[1], ahead, wallAhead)
		}
		if i > 0 {
			if d := max(abs64(int64(p[0]-pts[i-1][0])), abs64(int64(p[1]-pts[i-1][1]))); d < 32 || d > 48 {
				t.Errorf("pieces %d and %d stand %d apart on the larger axis, want touching (32 to 48)", i-1, i, d)
			}
		}
	}
	if w.e.wallPiece(w.b, w.d.con) != w.d.drag {
		t.Fatal("the constructor's wall piece is not the tooth")
	}
	if used := w.e.walls(w.b, 5); used != 0 {
		t.Fatalf("a second segment within wallEvery (%d actions)", used)
	}
}

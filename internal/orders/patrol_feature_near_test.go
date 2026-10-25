package orders

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// TestPatrolFeatureRestrictedScanMatchesFullScan locks the Modern patrol's
// scored reclaim pick under ForEachFeatureNear: a traversal that honours the region
// — skipping every feature outside the square FeatureRegion describes — gives
// the same pick as one that visits every feature, for random maps, builders,
// sight radii (negative included) and positions out to the edges of the
// coordinate word, where a wrapped difference is the trap.
func TestPatrolFeatureRestrictedScanMatchesFullScan(t *testing.T) {
	var features []FeatureView
	var adapter *WorldQueryAdapter
	restricted := 0
	adapter = NewWorldQueryAdapter(WorldQueryAdapterConfig{ForEachFeature: func(visit func(FeatureView) bool) {
		x, z, radius, ok := adapter.FeatureRegion()
		r := int64(radius) << 16
		for _, f := range features {
			if ok {
				dx := int64(int32(f.X.Raw())) - int64(int32(x.Raw()))
				dz := int64(int32(f.Z.Raw())) - int64(int32(z.Raw()))
				if radius < 0 || dx < -r || dx > r || dz < -r || dz > r {
					restricted++
					continue
				}
			}
			if visit(f) {
				return
			}
		}
	}})
	b := &QueueBinding{World: adapter}
	seed := uint32(5)
	next := func(n uint32) uint32 {
		seed = seed*1664525 + 1013904223
		return (seed >> 8) % n
	}
	coord := func() numeric.Fixed {
		if next(8) == 0 {
			// Near the ends of the 32-bit word: a difference must not wrap
			// into a small one.
			return numeric.Fixed(int64(int32(0x7fff0000) - int32(next(1<<20))*int32(next(3)-1)*0x800))
		}
		return numeric.Fixed(int64(next(4096)) << 16)
	}
	resources := ResourceView{Capacity: [2]float32{1000, 1000}}
	picks := 0
	for round := 0; round < 400; round++ {
		features = features[:0]
		for i, n := 0, int(next(80)); i < n; i++ {
			features = append(features, FeatureView{
				ID: uint16(next(4)), CX: int32(next(256)), CZ: int32(next(256)),
				X: coord(), Z: coord(), Metal: int32(next(3)), Energy: int32(next(3)),
				Reclaimable: next(5) != 0, Autoreclaimable: next(5) != 0,
			})
		}
		u := &units.Unit{X: coord(), Z: coord(), Def: &content.UnitDef{SightDistance: int32(next(900)) - 50}}
		if round%2 == 0 {
			// Candidates on the boundary: on an axis at exactly the radius,
			// which the distance test admits, and one raw unit beyond it.
			r := int64(u.Def.SightDistance) << 16
			for _, d := range []int64{r, r + 1, -r, -r - 1} {
				f := FeatureView{CX: int32(next(256)), CZ: int32(next(256)), Metal: 1, Reclaimable: true, Autoreclaimable: true}
				if next(2) == 0 {
					f.X, f.Z = u.X+numeric.Fixed(d), u.Z
				} else {
					f.X, f.Z = u.X, u.Z+numeric.Fixed(d)
				}
				features = append(features, f)
			}
			for i := range features[:len(features)-4] {
				features[i].Reclaimable = false // leave the boundary ones as the only candidates
			}
		}
		// A random leg exercises the route leash and the score's detour on
		// the same traversal; claims and memory stay empty.
		offset := func() numeric.Fixed { return numeric.Fixed(int64(int32(next(2048))-1024) << 16) }
		leg := &Node{ID: rowRepairPatrol, GoalX: u.X + offset(), GoalZ: u.Z + offset()}
		q := &Queue{primary: []*Node{leg, {ID: rowRepairPatrol, GoalX: coord(), GoalZ: coord()}}}
		if round%3 == 0 {
			q.primary = q.primary[:1] // no previous waypoint: no leash
		}
		s := newModernPatrolScore(q, u, leg)
		near := modernPatrolFeature(b, &s, resources, nil, true)
		full := modernPatrolFeature(b, &s, resources, nil, false)
		if near != full {
			t.Fatalf("round %d: the restricted scan picked %+v, the full scan %+v", round, near, full)
		}
		if full.found {
			picks++
		}
	}
	if picks < 100 || restricted == 0 {
		t.Fatalf("%d picks and %d skipped features exercised too little", picks, restricted)
	}
	if _, _, _, ok := adapter.FeatureRegion(); ok {
		t.Fatal("the region outlived its traversal")
	}
}

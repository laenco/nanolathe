package mapgen

import "slices"

// placed is one feature on the map, by its footprint's anchor (top-left)
// cell, the cell a TNT stores it in [fmt tnt] "Attribute map".
type placed struct {
	feature *Feature
	x, y    int
}

// Deposits per player in the near band, the middle band, and shared with
// each neighbouring enemy, by the metal parameter (few, normal, many).
var (
	depositsNear   = [MaxMetal + 1]int{2, 3, 4}
	depositsMiddle = [MaxMetal + 1]int{2, 3, 4}
	depositsShared = [MaxMetal + 1]int{0, 1, 2}
)

// The bands, per mille of the reach: half the shortest travel cost between
// any two enemy starts, so the same for everyone. A player's own deposits lie
// well inside it; shared ones lie beyond it, where no player is nearer than
// the closest pair of enemies is to each other.
const (
	nearLo, nearHi     = 220, 420
	middleLo, middleHi = 500, 800
	sharedLo, sharedHi = 1000, 1400
)

// features tracks what a candidate has placed and the cells it covers.
type features struct {
	w, h     int
	list     []placed
	occupied []bool // covered, or kept clear round a deposit
	blocking []bool
}

func newFeatures(w, h int) *features {
	return &features{w: w, h: h, occupied: make([]bool, w*h), blocking: make([]bool, w*h)}
}

// free reports whether a footprint and a margin round it are unoccupied and
// inside the play area.
func (f *features) free(x, y, fx, fz, margin int) bool {
	if x-margin < 0 || y-margin < 0 || x+fx+margin > f.w-voidRight || y+fz+margin > f.h-voidBottom {
		return false
	}
	for yy := y - margin; yy < y+fz+margin; yy++ {
		for xx := x - margin; xx < x+fx+margin; xx++ {
			if f.occupied[yy*f.w+xx] {
				return false
			}
		}
	}
	return true
}

// put places a feature and covers its footprint plus keep cells round it.
// A footprint of zero still holds its anchor cell, which the TNT gives it.
func (f *features) put(feat *Feature, x, y, keep int) {
	f.list = append(f.list, placed{feature: feat, x: x, y: y})
	fx, fz := max(1, feat.FootX), max(1, feat.FootZ)
	for yy := max(0, y-keep); yy < min(f.h, y+fz+keep); yy++ {
		for xx := max(0, x-keep); xx < min(f.w, x+fx+keep); xx++ {
			f.occupied[yy*f.w+xx] = true
		}
	}
	if feat.Blocking {
		for yy := y; yy < y+fz; yy++ {
			for xx := x; xx < x+fx; xx++ {
				f.blocking[yy*f.w+xx] = true
			}
		}
	}
}

// padRange is the steepest ground a deposit's pad is cut into: the corner
// difference over its footprint and the ring round it.
const padRange = 24

// padSpot reports whether a footprint and the ring round it are dry land
// gentle enough to level a pad on.
func padSpot(t *terrain, x, y, fx, fz int) bool {
	if x < 1 || y < 1 || x+fx+2 > t.w || y+fz+2 > t.h {
		return false
	}
	lo, hi := int32(1<<30), int32(-1)
	for yy := y - 1; yy <= y+fz+1; yy++ {
		for xx := x - 1; xx <= x+fx+1; xx++ {
			v := t.height[yy*t.w+xx]
			lo, hi = min(lo, v), max(hi, v)
		}
	}
	return lo > t.sea+1 && hi-lo <= padRange
}

// levelPad flattens the ground under a footprint and the ring round it to
// its mean, so an extractor can be built there, and eases the next ring
// halfway towards it.
func (t *terrain) levelPad(x, y, fx, fz int) {
	var sum, n int64
	for yy := y - 1; yy <= y+fz+1; yy++ {
		for xx := x - 1; xx <= x+fx+1; xx++ {
			sum += int64(t.height[yy*t.w+xx])
			n++
		}
	}
	level := int32(sum / n)
	for yy := max(0, y-2); yy <= min(t.h-1, y+fz+2); yy++ {
		for xx := max(0, x-2); xx <= min(t.w-1, x+fx+2); xx++ {
			i := yy*t.w + xx
			if yy < y-1 || yy > y+fz+1 || xx < x-1 || xx > x+fx+1 {
				t.height[i] += (level - t.height[i]) / 2
				continue
			}
			t.height[i] = level
		}
	}
}

// placeDeposits gives every player the same deposits: for each band, target
// travel costs are drawn once for everyone, and each player takes the free
// spot in their own zone whose cost is nearest each target, levelled into a
// pad. Shared
// deposits go where two neighbouring enemies are equally far. It reports
// false when some player has no room for their share.
func placeDeposits(p Params, w World, t *terrain, f *features, starts []point, dist [][]int32, owner []int8, reach int32, r *stream) bool {
	if w.Deposit.Name == "" {
		return true // the ground is the metal
	}
	dep := &w.Deposit
	fx, fz := max(1, dep.FootX), max(1, dep.FootZ)
	spacing := 4
	take := func(player int, target int32, lo, hi int32) bool {
		best, bestGap := -1, int32(1<<30)
		d := dist[player]
		for y := 0; y+fz <= t.h; y++ {
			for x := 0; x+fx <= t.w; x++ {
				i := y*t.w + x
				if owner[i] != int8(player) || d[i] < lo || d[i] > hi {
					continue
				}
				gap := d[i] - target
				if gap < 0 {
					gap = -gap
				}
				if gap >= bestGap || !f.free(x, y, fx, fz, spacing) || !padSpot(t, x, y, fx, fz) {
					continue
				}
				best, bestGap = i, gap
			}
		}
		if best < 0 {
			return false
		}
		t.levelPad(best%t.w, best/t.w, fx, fz)
		f.put(dep, best%t.w, best/t.w, spacing)
		return true
	}
	for _, band := range []struct {
		count  int
		lo, hi int32
	}{
		{depositsNear[p.Metal], nearLo, nearHi},
		{depositsMiddle[p.Metal], middleLo, middleHi},
	} {
		lo, hi := reach*band.lo/1000, reach*band.hi/1000
		for k := 0; k < band.count; k++ {
			target := lo + int32(r.intn(int(hi-lo)+1))
			for player := range starts {
				if !take(player, target, lo, hi) {
					return false
				}
			}
		}
	}
	shared := depositsShared[p.Metal]
	if shared == 0 {
		return true
	}
	for a := range starts {
		for b := a + 1; b < len(starts); b++ {
			if teamOf(p, a) == teamOf(p, b) || !neighbours(owner, t.w, t.h, a, b) {
				continue
			}
			for k := 0; k < shared; k++ {
				placeShared(t, f, dep, dist[a], dist[b], reach, r)
			}
		}
	}
	return true
}

// placeShared puts one deposit where two starts are equally far, within the
// shared band of both, levelled into a pad; none when there is no room.
func placeShared(t *terrain, f *features, dep *Feature, da, db []int32, reach int32, r *stream) {
	fx, fz := max(1, dep.FootX), max(1, dep.FootZ)
	lo, hi := reach*sharedLo/1000, reach*sharedHi/1000
	var spots []int
	for y := 0; y+fz <= t.h; y++ {
		for x := 0; x+fx <= t.w; x++ {
			i := y*t.w + x
			if da[i] < lo || da[i] > hi || db[i] < lo || db[i] > hi {
				continue
			}
			if gap := da[i] - db[i]; gap > 30 || gap < -30 {
				continue
			}
			if f.free(x, y, fx, fz, 4) && padSpot(t, x, y, fx, fz) {
				spots = append(spots, i)
			}
		}
	}
	if len(spots) == 0 {
		return
	}
	i := spots[r.intn(len(spots))]
	t.levelPad(i%t.w, i/t.w, fx, fz)
	f.put(dep, i%t.w, i/t.w, 4)
}

// neighbours reports whether two zones touch.
func neighbours(owner []int8, w, h, a, b int) bool {
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o := owner[y*w+x]
			if o != int8(a) && o != int8(b) {
				continue
			}
			other := int8(a + b - int(o))
			if x+1 < w && owner[y*w+x+1] == other || y+1 < h && owner[(y+1)*w+x] == other {
				return true
			}
		}
	}
	return false
}

// placeVegetation scatters the world's trees and foliage in clusters a
// low-frequency field draws, and a few rocks, on dry gentle land away from
// the bases. Each kind is chosen by how often the world's maps use it.
func placeVegetation(w World, t *terrain, f *features, starts []point, seed uint64) {
	r := newStream(mix(seed, 40))
	pick := func(list []Feature) *Feature {
		total := 0
		for _, ft := range list {
			total += max(1, ft.Weight)
		}
		n := r.intn(total)
		for i := range list {
			n -= max(1, list[i].Weight)
			if n < 0 {
				return &list[i]
			}
		}
		return nil
	}
	clearRadius := roomCells
	nearBase := func(x, y int) bool {
		for _, s := range starts {
			if dx, dy := x-s.x, y-s.y; dx*dx+dy*dy <= clearRadius*clearRadius {
				return true
			}
		}
		return false
	}
	period := int64(max(12, t.w/7))
	for y := 0; y < t.h; y += 2 {
		for x := 0; x < t.w; x += 2 {
			if nearBase(x, y) {
				continue
			}
			forest := int64(fbm(mix(seed, 41), int64(x)<<16, int64(y)<<16, period, 3, 128))
			var list []Feature
			switch roll := int64(r.intn(1000)); {
			case len(w.Trees) > 0 && forest > 41000 && roll < (forest-41000)/18:
				list = w.Trees
			case len(w.Foliage) > 0 && forest > 33000 && roll < (forest-33000)/30:
				list = w.Foliage
			case len(w.Rocks) > 0 && roll < 3:
				list = w.Rocks
			default:
				continue
			}
			feat := pick(list)
			if feat == nil {
				continue
			}
			fx, fz := max(1, feat.FootX), max(1, feat.FootZ)
			if !f.free(x, y, fx, fz, 0) || !gentleLand(t, x, y, fx, fz) {
				continue
			}
			f.put(feat, x, y, 0)
		}
	}
}

// gentleLand reports whether a footprint is dry with a corner difference
// of 10 or less: trees do not grow on cliffs or under water.
func gentleLand(t *terrain, x, y, fx, fz int) bool {
	for yy := y; yy < y+fz; yy++ {
		for xx := x; xx < x+fx; xx++ {
			if xx+1 >= t.w || yy+1 >= t.h {
				return false
			}
			a, b := t.height[yy*t.w+xx], t.height[yy*t.w+xx+1]
			c, d := t.height[(yy+1)*t.w+xx], t.height[(yy+1)*t.w+xx+1]
			if min(a, b, c, d) <= t.sea || max(a, b, c, d)-min(a, b, c, d) > 10 {
				return false
			}
		}
	}
	return true
}

// sortPlaced orders the features as the TNT will list them: by anchor
// cell, so equal maps encode equally.
func sortPlaced(list []placed) {
	slices.SortFunc(list, func(a, b placed) int {
		if a.y != b.y {
			return a.y - b.y
		}
		return a.x - b.x
	})
}

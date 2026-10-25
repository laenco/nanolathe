package mapgen

// Balance is how evenly a candidate treats its players, each spread the
// difference between the best and the worst player per mille of the mean.
type Balance struct {
	// Path is each start's travel cost to the nearest enemy start (a
	// teammate is no enemy), by the ground class's passability and slope
	// costs (paths.go).
	Path Measure
	// Room is each start's building room: the level, dry cells within
	// roomCells of travel of it that no feature occupies and no deposit's
	// spacing ring keeps clear, a 2x2 block counting when its heights
	// differ by at most 2.
	Room Measure
	// Metal is the deposit metal in each start's own zone (the cells its
	// units reach first) within metalReach per mille of the reach, the
	// travel cost halfway to the nearest enemy.
	Metal Measure
	// Score weighs the three spreads to rank candidates; lower is fairer.
	// It compares this layout's starts with each other and promises nothing
	// beyond these three measurements.
	Score int
}

// Measure is one measurement of a layout's starts: each start's value in
// start order, and their spread.
type Measure struct {
	Values []int64
	// Spread is (max - min) per mille of the mean.
	Spread int
}

// roomCells is how far from a base its building room is counted, and how
// far round a base no tree grows, so trees never decide whose room is
// larger; roomCost is the same distance in travel cost.
const (
	roomCells = 30
	roomCost  = roomCells * stepStraight
)

// metalReach is how far from a base its metal is counted, per mille of the
// reach: past every player's own deposits, short of the shared ones.
const metalReach = 900

// measure compares the players of a finished candidate under the ground
// class: travel to the nearest enemy, building room and the metal in each
// player's own zone within the same travel cost of every base, so that a
// teammate's deposits are not counted twice. It reports false when some
// start cannot reach every enemy.
func measure(p Params, w World, t *terrain, f *features, starts []point, reach int32) (Balance, bool) {
	mc := w.Classes[0]
	tiers := footprintTiers(cellTiers(t.height, t.w, t.h, t.sea, w.Lava, f.blocking, mc), t.w, t.h, mc)
	n := len(starts)
	anchors := make([]int, n)
	for i, s := range starts {
		anchors[i] = anchorOf(s, t.w, mc)
	}
	_, owner := travel(tiers, t.w, t.h, anchors)
	path, room, metal := make([]int64, n), make([]int64, n), make([]int64, n)
	for i := range starts {
		dist, _ := travel(tiers, t.w, t.h, anchors[i:i+1])
		nearest := int32(-1)
		for j, o := range starts {
			if teamOf(p, j) == teamOf(p, i) {
				continue
			}
			d := dist[anchorOf(o, t.w, mc)]
			if d == unreached {
				return Balance{}, false
			}
			if nearest < 0 || d < nearest {
				nearest = d
			}
		}
		path[i] = int64(nearest)
		for c, d := range dist {
			if d == unreached || d > roomCost || f.occupied[c] {
				continue
			}
			x, y := c%t.w, c/t.w
			if x+1 < t.w && y+1 < t.h {
				a, b := t.height[c], t.height[c+1]
				cc, dd := t.height[c+t.w], t.height[c+t.w+1]
				if min(a, b, cc, dd) > t.sea && max(a, b, cc, dd)-min(a, b, cc, dd) <= 2 {
					room[i]++
				}
			}
		}
		for _, pl := range f.list {
			if pl.feature.Metal == 0 || w.Deposit.Name == "" || pl.feature.Name != w.Deposit.Name {
				continue
			}
			c := pl.y*t.w + pl.x
			if d := dist[c]; owner[c] == int8(i) && d != unreached && d <= reach*metalReach/1000 {
				metal[i] += int64(pl.feature.Metal * pl.feature.FootX * pl.feature.FootZ)
			}
		}
	}
	b := Balance{
		Path:  Measure{Values: path, Spread: spread(path)},
		Room:  Measure{Values: room, Spread: spread(room)},
		Metal: Measure{Values: metal, Spread: spread(metal)},
	}
	b.Score = 4*b.Path.Spread + 2*b.Room.Spread + 3*b.Metal.Spread
	return b, true
}

// spread is (max - min) per mille of the mean.
func spread(v []int64) int {
	if len(v) == 0 {
		return 0
	}
	lo, hi, sum := v[0], v[0], int64(0)
	for _, x := range v {
		lo, hi, sum = min(lo, x), max(hi, x), sum+x
	}
	mean := sum / int64(len(v))
	if mean == 0 {
		if hi == lo {
			return 0
		}
		return 1000
	}
	return int((hi - lo) * 1000 / mean)
}

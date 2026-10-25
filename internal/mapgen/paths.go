package mapgen

// tier is a cell's passability for one movement class, the engine's own
// three tiers [04 §6.1 R-DOC04-B][04 R-SLOPE-01 §2].
type tier uint8

const (
	blocked tier = iota
	steep
	clear
)

// cellTiers classifies every cell as the engine's per-cell chain does: a
// blocking feature blocks, and so does the void the engine makes at map load
// [03 §2.2] — the two rightmost columns, the north and south strips where
// raw height reaches past the edge [03 R-TERR-01 §2], and on a lava world
// every cell whose lowest corner is at or below the sea; then the cell's
// lowest and highest corner (its own 2x2 height neighbourhood) against the
// class's water depths; then the land or water slope pair on their
// difference — bad or less is clear, above max is blocked, between is steep.
func cellTiers(height []int32, w, h int, sea int32, lava bool, blockedCell []bool, mc MoveClass) []tier {
	out := make([]tier, w*h)
	at := func(x, y int) int32 { return height[min(y, h-1)*w+min(x, w-1)] }
	// Each column's strips end at the first row that fails the test: the
	// north strip is the rows above north[x], and the south strip is the
	// rows from south[x] to the second-last, each voided for the row below
	// it, so the bottom row never is [03 R-TERR-01 §2].
	north, south := make([]int, w), make([]int, w)
	for x := 0; x < w; x++ {
		z := 0
		for z < h && z*16 < int(at(x, z)>>1) {
			z++
		}
		north[x] = z
		z = h - 1
		for z > 0 && (h-1-z)*16+int(at(x, z)>>1) < 112 {
			z--
		}
		south[x] = z
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if blockedCell != nil && blockedCell[i] || x >= w-2 || y < north[x] || y >= south[x] && y < h-1 {
				continue
			}
			a, b, c, d := at(x, y), at(x+1, y), at(x, y+1), at(x+1, y+1)
			low, high := min(a, b, c, d), max(a, b, c, d)
			if lava && low <= sea || low < sea-int32(mc.MaxWaterDepth) || high > sea-int32(mc.MinWaterDepth) {
				continue
			}
			maxSlope, badSlope := int32(mc.MaxWaterSlope), int32(mc.BadWaterSlope)
			if low >= sea {
				maxSlope, badSlope = int32(mc.MaxSlope), int32(mc.BadSlope)
			}
			switch slope := high - low; {
			case slope <= badSlope:
				out[i] = clear
			case slope <= maxSlope:
				out[i] = steep
			}
		}
	}
	return out
}

// footprintTiers is the tier of a footprint anchored at each cell: the
// lowest tier inside it, and steep when any cell of the ring round it is
// below clear, the map's outside counting as blocked, as the engine's
// footprint classifier rules [04 R-SLOPE-01 §3].
func footprintTiers(cells []tier, w, h int, mc MoveClass) []tier {
	fx, fz := max(1, mc.FootX), max(1, mc.FootZ)
	out := make([]tier, w*h)
	minIn := func(x1, y1, x2, y2 int) tier {
		if x1 < 0 || y1 < 0 || x2 >= w || y2 >= h {
			return blocked
		}
		t := clear
		for y := y1; y <= y2; y++ {
			for x := x1; x <= x2; x++ {
				t = min(t, cells[y*w+x])
				if t == blocked {
					return blocked
				}
			}
		}
		return t
	}
	for y := 0; y+fz <= h; y++ {
		for x := 0; x+fx <= w; x++ {
			t := minIn(x, y, x+fx-1, y+fz-1)
			if t == clear && (minIn(x-1, y-1, x+fx, y-1) < clear || minIn(x-1, y+fz, x+fx, y+fz) < clear ||
				minIn(x-1, y-1, x-1, y+fz) < clear || minIn(x+fx, y-1, x+fx, y+fz) < clear) {
				t = steep
			}
			out[y*w+x] = t
		}
	}
	return out
}

// Step costs: a straight step 16, a diagonal 22, and a step onto a steep
// anchor 30 more, the engine's own search costs [04 R-PATH-01 §3]. Its
// turn and short-run penalties depend on a route's heading, which a cost
// per cell does not carry, so travel leaves them out: it measures the
// ground between starts, not the route a unit will take.
const (
	stepStraight = 16
	stepDiagonal = 22
	stepSteep    = 30
)

// StepCost is the travel cost of one straight step between cells: a
// Balance.Path value divided by it is a distance in cells.
const StepCost = stepStraight

// unreached marks a cell no source reaches.
const unreached = int32(-1)

// travel is the cheapest route cost from the sources to every cell over
// passable anchors (Dial's bucket queue, exact for these small integer
// costs), and which source each cell is nearest. A diagonal step tests only
// the anchor it lands on, as the engine's does [04 §7.1].
func travel(tiers []tier, w, h int, sources []int) (dist []int32, owner []int8) {
	dist = make([]int32, w*h)
	owner = make([]int8, w*h)
	for i := range dist {
		dist[i], owner[i] = unreached, -1
	}
	const span = stepDiagonal + stepSteep + 1
	buckets := make([][]int32, span)
	pending := 0
	for k, s := range sources {
		if s < 0 || s >= len(tiers) || tiers[s] == blocked {
			continue
		}
		if dist[s] == 0 {
			continue
		}
		dist[s], owner[s] = 0, int8(k)
		buckets[0] = append(buckets[0], int32(s))
		pending++
	}
	steps := [8]struct{ dx, dy, cost int32 }{
		{1, 0, stepStraight}, {-1, 0, stepStraight}, {0, 1, stepStraight}, {0, -1, stepStraight},
		{1, 1, stepDiagonal}, {-1, 1, stepDiagonal}, {1, -1, stepDiagonal}, {-1, -1, stepDiagonal},
	}
	for d := int32(0); pending > 0; d++ {
		b := &buckets[d%span]
		for len(*b) > 0 {
			i := (*b)[len(*b)-1]
			*b = (*b)[:len(*b)-1]
			pending--
			if dist[i] != d {
				continue // a cheaper route reached it first
			}
			x, y := i%int32(w), i/int32(w)
			for _, s := range steps {
				nx, ny := x+s.dx, y+s.dy
				if nx < 0 || ny < 0 || nx >= int32(w) || ny >= int32(h) {
					continue
				}
				n := ny*int32(w) + nx
				if tiers[n] == blocked {
					continue
				}
				cost := d + s.cost
				if tiers[n] == steep {
					cost += stepSteep
				}
				if dist[n] == unreached || cost < dist[n] {
					dist[n], owner[n] = cost, owner[i]
					nb := &buckets[cost%span]
					*nb = append(*nb, n)
					pending++
				}
			}
		}
	}
	return dist, owner
}

// anchorOf is the anchor cell of a class's footprint centred on p.
func anchorOf(p point, w int, mc MoveClass) int {
	return (p.y-max(1, mc.FootZ)/2)*w + p.x - max(1, mc.FootX)/2
}

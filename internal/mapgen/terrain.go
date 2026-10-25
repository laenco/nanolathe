package mapgen

import "slices"

// Cells per OTA size unit: one unit is 512 pixels, a cell 16.
const cellsPerUnit = 32

// baseRadius is the flat ground round a start, in cells: room for a
// commander's first buildings [fmt tnt] cells of 16 pixels.
const baseRadius = 14

// baseBlend is the ring over which a base's flat ground meets the terrain.
const baseBlend = 14

// The land's height range, the water's depth and the slope thermal erosion
// leaves, by relief (plains, rolling, hills, mountains). Heights are TNT
// bytes; a land vehicle's clear tier is a corner difference of 7 or less and
// its blocked tier above 15 (tanksh2/3 in the retail moveinfo.tdf), so the
// talus keeps plains and rolling land clear and lets hills and mountains
// grow steep slopes and walls.
var (
	reliefLand  = [MaxRelief + 1]int32{36, 70, 120, 185}
	reliefTalus = [MaxRelief + 1]int32{3, 4, 6, 10}
	ridgeShare  = [MaxRelief + 1]int64{0, 0, 90, 170} // of 256
	waterShare  = [MaxWater + 1]int{0, 110, 260}      // per mille of the map
)

// seaLevel is the water line of a map with water; a dry map has none.
const seaLevel = 40

// terrain is a candidate's heights in TNT bytes, cell by cell.
type terrain struct {
	w, h   int
	height []int32
	sea    int32
}

// makeTerrain builds the heights of one candidate: warped fractal land,
// ridges where the relief asks for mountains, the sea at the share of water
// asked for, flat bases at the starts and erosion to the relief's talus.
func makeTerrain(p Params, w, h int, seed uint64, starts []point) *terrain {
	t := &terrain{w: w, h: h, height: make([]int32, w*h)}
	noise := make([]int32, w*h)
	coarse := make([]int32, w*h)
	period := int64(max(16, w/3))
	warp := int64(max(4, w/12))
	mWeight := ridgeShare[p.Relief]
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			fx, fy := int64(x)<<16, int64(y)<<16
			wx := (int64(fbm(mix(seed, 1), fx, fy, int64(w/4), 3, 128)) - 32768) * warp >> 15
			wy := (int64(fbm(mix(seed, 2), fx, fy, int64(w/4), 3, 128)) - 32768) * warp >> 15
			sx, sy := fx+wx<<16, fy+wy<<16
			v := int64(fbm(mix(seed, 3), sx, sy, period, 6, 128))
			coarse[y*w+x] = fbm(mix(seed, 3), sx, sy, period, 3, 128)
			if mWeight > 0 {
				mask := int64(fbm(mix(seed, 4), fx, fy, int64(w/2), 2, 128))
				m := clamp64((mask-24000)*3, 0, 65535) * mWeight >> 8
				r := int64(ridged(mix(seed, 5), sx, sy, int64(max(12, w/5)), 4, 140))
				v += (r - v) * m >> 16
			}
			noise[y*w+x] = int32(v)
		}
	}
	// Stretch the noise's middle 96% over the whole range, so every seed
	// gives the relief's full contrast.
	sorted := slices.Clone(noise)
	slices.Sort(sorted)
	lo, hi := sorted[len(sorted)*2/100], sorted[len(sorted)*98/100]
	land := reliefLand[p.Relief]
	if share := waterShare[p.Water]; share > 0 {
		// The coast follows the coarse octaves alone and the fine ones fade
		// in over a band inland, so shores are smooth curves rather than a
		// spray of ponds and islets.
		sortedC := slices.Clone(coarse)
		slices.Sort(sortedC)
		loC, hiC := sortedC[len(sortedC)*2/100], sortedC[len(sortedC)*98/100]
		water := sortedC[len(sortedC)*share/1000]
		band := int64(max(1, (hiC-water)/5))
		t.sea = seaLevel
		for i := range noise {
			c := min(max(coarse[i], loC), hiC)
			if c < water {
				// Depth grows with the square of the distance below
				// the coast's level, so shallows fringe every shore
				// before the sea floor drops away.
				x := int64(water-c) * 256 / int64(max(1, water-loC))
				depth := int32(x * x * int64(seaLevel-4) >> 16)
				t.height[i] = seaLevel - 1 - depth
				continue
			}
			ramp := clamp64(int64(c-water)*256/band, 0, 256)
			v := int64(c) + (int64(min(max(noise[i], lo), hi))-int64(c))*ramp>>8
			t.height[i] = seaLevel + 1 + int32(max(0, v-int64(water))*int64(land)/int64(max(1, hi-water)))
		}
	} else {
		for i, v := range noise {
			v = min(max(v, lo), hi)
			t.height[i] = 1 + int32(int64(v-lo)*int64(land)/int64(max(1, hi-lo)))
		}
	}
	t.flattenBases(starts)
	t.erode(reliefTalus[p.Relief], 24)
	t.flattenBases(starts)
	t.erode(reliefTalus[p.Relief], 4)
	for i, v := range t.height {
		t.height[i] = min(max(v, 0), 255)
	}
	if t.sea > 0 {
		t.dropSpecks()
	}
	return t
}

// minPatch is the smallest pond or islet kept, in cells: anything smaller is
// noise at the shore rather than a place.
const minPatch = 48

// dropSpecks fills ponds and sinks islets smaller than minPatch.
func (t *terrain) dropSpecks() {
	seen := make([]bool, len(t.height))
	var patch, queue []int
	for i := range t.height {
		if seen[i] {
			continue
		}
		wet := t.height[i] < t.sea
		patch, queue = patch[:0], append(queue[:0], i)
		seen[i] = true
		for len(queue) > 0 {
			c := queue[len(queue)-1]
			queue = queue[:len(queue)-1]
			patch = append(patch, c)
			x, y := c%t.w, c/t.w
			for _, n := range [4]int{c - 1, c + 1, c - t.w, c + t.w} {
				switch {
				case n < 0 || n >= len(t.height) || seen[n] || (t.height[n] < t.sea) != wet:
				case (n == c-1 && x == 0) || (n == c+1 && x == t.w-1) || (n == c-t.w && y == 0):
				default:
					seen[n] = true
					queue = append(queue, n)
				}
			}
		}
		if len(patch) >= minPatch {
			continue
		}
		for _, c := range patch {
			if wet {
				t.height[c] = t.sea + 1
			} else {
				t.height[c] = t.sea - 2
			}
		}
	}
}

// flattenBases levels the ground round every start to its mean height, kept
// above the water, and blends it into the land around.
func (t *terrain) flattenBases(starts []point) {
	for _, s := range starts {
		var sum, n int64
		t.disc(s, baseRadius, func(i, _ int) { sum += int64(t.height[i]); n++ })
		level := int32(sum / max(1, n))
		if t.sea > 0 {
			level = max(level, t.sea+8)
		}
		t.disc(s, baseRadius+baseBlend, func(i, d int) {
			if d <= baseRadius {
				t.height[i] = level
				return
			}
			weight := int32((baseRadius + baseBlend - d) * 256 / baseBlend)
			t.height[i] += (level - t.height[i]) * weight >> 8
		})
	}
}

// widenBase levels the ground round one start out to radius at the level
// its base already has, blended into the land around as flattenBases
// blends it: room for a base that measured short of its rivals'.
func (t *terrain) widenBase(s point, radius int) {
	level := t.height[s.y*t.w+s.x]
	t.disc(s, radius+baseBlend, func(i, d int) {
		if d <= radius {
			t.height[i] = level
			return
		}
		weight := int32((radius + baseBlend - d) * 256 / baseBlend)
		t.height[i] += (level - t.height[i]) * weight >> 8
	})
}

// disc visits the cells within r of p with their distance.
func (t *terrain) disc(p point, r int, visit func(i, d int)) {
	for y := max(0, p.y-r); y <= min(t.h-1, p.y+r); y++ {
		for x := max(0, p.x-r); x <= min(t.w-1, p.x+r); x++ {
			dx, dy := x-p.x, y-p.y
			if d := isqrt(dx*dx + dy*dy); d <= r {
				visit(y*t.w+x, d)
			}
		}
	}
}

// erode moves height from a cell to a lower neighbour wherever their
// difference exceeds talus, alternating the sweep's direction so neither
// side of the map is favoured.
func (t *terrain) erode(talus int32, iterations int) {
	relax := func(a, b int) {
		d := t.height[a] - t.height[b]
		switch {
		case d > talus:
			m := (d - talus) / 2
			t.height[a] -= m
			t.height[b] += m
		case -d > talus:
			m := (-d - talus) / 2
			t.height[a] += m
			t.height[b] -= m
		}
	}
	for it := 0; it < iterations; it++ {
		if it%2 == 0 {
			for y := 0; y < t.h; y++ {
				for x := 0; x < t.w; x++ {
					i := y*t.w + x
					if x+1 < t.w {
						relax(i, i+1)
					}
					if y+1 < t.h {
						relax(i, i+t.w)
					}
				}
			}
			continue
		}
		for y := t.h - 1; y >= 0; y-- {
			for x := t.w - 1; x >= 0; x-- {
				i := y*t.w + x
				if x > 0 {
					relax(i, i-1)
				}
				if y > 0 {
					relax(i, i-t.w)
				}
			}
		}
	}
}

func isqrt(v int) int {
	if v <= 0 {
		return 0
	}
	r := 0
	for bit := 1 << 30; bit > 0; bit >>= 2 {
		if v >= r+bit {
			v -= r + bit
			r = r>>1 + bit
		} else {
			r >>= 1
		}
	}
	return r
}

func clamp64(v, lo, hi int64) int64 { return min(max(v, lo), hi) }

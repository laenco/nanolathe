package mapgen

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"math/bits"
	"slices"
)

// signature is what a 32x32 tile's art has to show, read from the 2x2 cells
// it covers and the 3x3 corner heights round them: which cells are under
// water, how deep the water or how high the land, how steep the ground and
// which way the slope faces. Retail water and cliffs are nothing but tile
// art [03 §6], so a tile whose signature matches the ground it
// is laid on draws the right shore, cliff and depth there.
type signature uint16

const (
	sigLevelShift = 4 // 2 bits: water depth when all four cells are wet, else the land's height band
	sigSlopeShift = 6 // 2 bits: flat, gentle, steep, cliff
	sigDirShift   = 8 // 4 bits: rising east, west, south, north
)

// relaxMasks are the signature's parts kept when no source tile matches all
// of it: first everything, then without the slope's direction, then water
// and slope only, then the water cells alone.
var relaxMasks = [...]signature{0x0FFF, 0x00FF, 0x00CF, 0x000F}

// bands splits a map's land heights into thirds.
type bands struct{ lo, hi int32 }

func landBands(n int, at func(i int) int32, sea int32) bands {
	var land []int32
	for i := 0; i < n; i++ {
		if v := at(i); v >= sea {
			land = append(land, v)
		}
	}
	if len(land) == 0 {
		return bands{lo: sea + 1, hi: sea + 2}
	}
	slices.Sort(land)
	return bands{lo: land[len(land)/3], hi: land[len(land)*2/3]}
}

// tileSignature reads the signature of tile (tx, ty) of a map w cells wide
// and h high.
func tileSignature(at func(x, y int) int32, w, h, tx, ty int, sea int32, b bands) signature {
	x0, y0 := 2*tx, 2*ty
	cell := func(x, y int) int32 { return at(min(x, w-1), min(y, h-1)) }
	var sig signature
	var landSum, landN, deepest int32
	for j := 0; j < 2; j++ {
		for i := 0; i < 2; i++ {
			v := cell(x0+i, y0+j)
			if v < sea {
				sig |= 1 << (j*2 + i)
				deepest = max(deepest, sea-v)
				continue
			}
			landSum += v
			landN++
		}
	}
	level := signature(0)
	switch {
	case landN == 0 && deepest > 20:
		level = 2
	case landN == 0 && deepest > 8:
		level = 1
	case landN > 0 && landSum/landN >= b.hi:
		level = 2
	case landN > 0 && landSum/landN >= b.lo:
		level = 1
	}
	lo, hi := int32(1<<30), int32(-1)
	var left, right, top, bottom int32
	for j := 0; j < 3; j++ {
		for i := 0; i < 3; i++ {
			v := cell(x0+i, y0+j)
			lo, hi = min(lo, v), max(hi, v)
			switch i {
			case 0:
				left += v
			case 2:
				right += v
			}
			switch j {
			case 0:
				top += v
			case 2:
				bottom += v
			}
		}
	}
	// The slope over the tile's 3x3 corners spans two cells, so these
	// classes are about a land vehicle's per-cell tiers doubled: level
	// ground, ground it crosses at full speed, ground it climbs slowly, and
	// walls (tanksh2/3 bad slope 7, max slope 15 in the retail moveinfo.tdf).
	slope := signature(3)
	switch r := hi - lo; {
	case r <= 6:
		slope = 0
	case r <= 14:
		slope = 1
	case r <= 30:
		slope = 2
	}
	var dir signature
	if slope >= 2 {
		if d := right - left; d > 12 {
			dir |= 1
		} else if d < -12 {
			dir |= 2
		}
		if d := bottom - top; d > 12 {
			dir |= 4
		} else if d < -12 {
			dir |= 8
		}
	}
	return sig | level<<sigLevelShift | slope<<sigSlopeShift | dir<<sigDirShift
}

// tileRef is one tile position of one source map.
type tileRef struct{ src, tx, ty int32 }

// buckets finds tile positions by signature at each relaxation level.
type buckets [len(relaxMasks)]map[signature][]tileRef

func newBuckets() buckets {
	var b buckets
	for l := range b {
		b[l] = map[signature][]tileRef{}
	}
	return b
}

func (b *buckets) add(sig signature, r tileRef) {
	for l, mask := range relaxMasks {
		b[l][sig&mask] = append(b[l][sig&mask], r)
	}
}

// tileIndex finds source tiles by signature.
type tileIndex struct {
	sources []Source
	sigs    [][]signature // per source, per tile position
	hashes  [][]uint64    // per source, per tile number: its art's hash
	edges   [][]edges     // per source, per tile number
	all     buckets
	own     []buckets  // per source
	colour  [][3]int64 // per source, the mean colour of its dry tiles
}

// edges summarises a tile's four borders for matching neighbours: each
// border as edgeSegments runs of pixels, each run's mean colour.
type edges [4][edgeSegments][3]int16

const (
	edgeLeft = iota
	edgeRight
	edgeTop
	edgeBottom
	edgeSegments = 8
)

func tileEdges(art []byte, palette *[256][3]byte) edges {
	var e edges
	const run = 32 / edgeSegments
	for k := 0; k < 32; k++ {
		seg := k / run
		for side, px := range [4]byte{art[k*32], art[k*32+31], art[k], art[31*32+k]} {
			c := palette[px]
			for ch := 0; ch < 3; ch++ {
				e[side][seg][ch] += int16(c[ch])
			}
		}
	}
	for side := range e {
		for seg := range e[side] {
			for ch := range e[side][seg] {
				e[side][seg][ch] /= run
			}
		}
	}
	return e
}

// seam is how badly border a of one tile meets border b of its neighbour:
// summed squared differences of the runs' colours, scaled to the 32 pixels
// a border holds.
func seam(a, b *[edgeSegments][3]int16) int {
	cost := 0
	for seg := range a {
		for ch := 0; ch < 3; ch++ {
			d := int(a[seg][ch]) - int(b[seg][ch])
			cost += d * d
		}
	}
	return cost * (32 / edgeSegments)
}

// buildIndex reads every source tile's signature. Which of a source tile's
// cells are water is read from its art, not its heights: an author's shore
// does not always follow the height map, and a lake or ice painted on high
// ground would otherwise land on the generated map's dry land. The world's
// water colours are those far more common under its maps' water than on
// their land. Tiles over cells a source marks to avoid are left out.
func buildIndex(sources []Source, palette *[256][3]byte) (*tileIndex, error) {
	idx := &tileIndex{sources: sources, all: newBuckets()}
	var wetHist, dryHist [256]int64
	var wetTotal, dryTotal int64
	for _, src := range sources {
		tw, th := src.Width/2, src.Height/2
		if src.Width%2 != 0 || src.Height%2 != 0 || len(src.Heights) != src.Width*src.Height || len(src.TileIndices) != tw*th || len(src.TileGraphics)%1024 != 0 ||
			src.Avoid != nil && len(src.Avoid) != src.Width*src.Height {
			return nil, fmt.Errorf("mapgen: source %q: inconsistent sizes", src.Name)
		}
		tiles := len(src.TileGraphics) / 1024
		hashes := make([]uint64, tiles)
		tileEdgeList := make([]edges, tiles)
		for i := range hashes {
			art := src.TileGraphics[i*1024 : (i+1)*1024]
			h := fnv.New64a()
			h.Write(art)
			hashes[i] = h.Sum64()
			tileEdgeList[i] = tileEdges(art, palette)
		}
		idx.hashes = append(idx.hashes, hashes)
		idx.edges = append(idx.edges, tileEdgeList)
		at := func(x, y int) int32 { return int32(src.Heights[y*src.Width+x]) }
		b := landBands(len(src.Heights), func(i int) int32 { return int32(src.Heights[i]) }, int32(src.SeaLevel))
		sigs := make([]signature, tw*th)
		for ty := 0; ty < th; ty++ {
			for tx := 0; tx < tw; tx++ {
				sig := tileSignature(at, src.Width, src.Height, tx, ty, int32(src.SeaLevel), b)
				sigs[ty*tw+tx] = sig
				n := int(src.TileIndices[ty*tw+tx])
				if n >= tiles || avoided(src, tx, ty) || (sig&0xF != 0 && sig&0xF != 0xF) {
					continue
				}
				hist, total := &dryHist, &dryTotal
				if sig&0xF == 0xF {
					hist, total = &wetHist, &wetTotal
				}
				for k := 0; k < 1024; k += 7 {
					hist[src.TileGraphics[n*1024+k]]++
					(*total)++
				}
			}
		}
		idx.sigs = append(idx.sigs, sigs)
	}
	// A colour is water's when it is at least four times as frequent, by
	// share, under water as on land, and land's the other way round; the
	// rest could be either.
	var looks [256]int8
	if wetTotal > 0 && dryTotal > 0 {
		for c := range looks {
			switch {
			case wetHist[c]*dryTotal > 4*dryHist[c]*wetTotal:
				looks[c] = 1
			case dryHist[c]*wetTotal > 4*wetHist[c]*dryTotal:
				looks[c] = -1
			}
		}
	}
	for s, src := range sources {
		tw, th := src.Width/2, src.Height/2
		tiles := len(src.TileGraphics) / 1024
		own := newBuckets()
		var colour [3]int64
		var counted int64
		for ty := 0; ty < th; ty++ {
			for tx := 0; tx < tw; tx++ {
				n := int(src.TileIndices[ty*tw+tx])
				if n >= tiles {
					continue
				}
				sig := idx.sigs[s][ty*tw+tx]
				heights := sig & 0xF
				art := heights
				if wetTotal > 0 && dryTotal > 0 {
					art = artMask(src.TileGraphics[n*1024:(n+1)*1024], &looks, heights)
				}
				sig = sig&^0xF | art
				if (art == 0xF || art == 0) && heights != art {
					sig &^= 3 << sigLevelShift // painted water is shallow; painted land is low
				}
				idx.sigs[s][ty*tw+tx] = sig
				if avoided(src, tx, ty) {
					continue
				}
				ref := tileRef{int32(s), int32(tx), int32(ty)}
				idx.all.add(sig, ref)
				own.add(sig, ref)
				if sig&0xF == 0 {
					for k := 0; k < 1024; k += 61 {
						c := palette[src.TileGraphics[n*1024+k]]
						colour[0], colour[1], colour[2] = colour[0]+int64(c[0]), colour[1]+int64(c[1]), colour[2]+int64(c[2])
						counted++
					}
				}
			}
		}
		if counted > 0 {
			colour = [3]int64{colour[0] / counted, colour[1] / counted, colour[2] / counted}
		}
		idx.own = append(idx.own, own)
		idx.colour = append(idx.colour, colour)
	}
	return idx, nil
}

// artMask is which quarters of a tile its art shows as water: those where
// most of the sampled pixels have water's colours, not those where most
// have land's, and as the heights say where the colours could be either.
func artMask(art []byte, looks *[256]int8, heights signature) signature {
	mask := heights
	for q := 0; q < 4; q++ {
		x0, y0 := (q%2)*16, (q/2)*16
		wet, dry := 0, 0
		for y := y0; y < y0+16; y += 2 {
			for x := x0; x < x0+16; x += 2 {
				switch looks[art[y*32+x]] {
				case 1:
					wet++
				case -1:
					dry++
				}
			}
		}
		switch {
		case wet > 32:
			mask |= 1 << q
		case dry > 32:
			mask &^= 1 << q
		}
	}
	return mask
}

func avoided(src Source, tx, ty int) bool {
	if src.Avoid == nil {
		return false
	}
	for j := 0; j < 2; j++ {
		for i := 0; i < 2; i++ {
			if src.Avoid[(2*ty+j)*src.Width+2*tx+i] {
				return true
			}
		}
	}
	return false
}

func (idx *tileIndex) valid(r tileRef) bool {
	src := &idx.sources[r.src]
	return r.tx >= 0 && r.ty >= 0 && int(r.tx) < src.Width/2 && int(r.ty) < src.Height/2 &&
		int(src.TileIndices[int(r.ty)*(src.Width/2)+int(r.tx)]) < len(src.TileGraphics)/1024 && !avoided(*src, int(r.tx), int(r.ty))
}

func (idx *tileIndex) sig(r tileRef) signature {
	return idx.sigs[r.src][int(r.ty)*(idx.sources[r.src].Width/2)+int(r.tx)]
}

// art is a referenced tile's 1024 palette indices.
func (idx *tileIndex) art(r tileRef) []byte {
	src := &idx.sources[r.src]
	n := int(src.TileIndices[int(r.ty)*(src.Width/2)+int(r.tx)])
	return src.TileGraphics[n*1024 : (n+1)*1024]
}

func (idx *tileIndex) edgesOf(r tileRef) *edges {
	src := &idx.sources[r.src]
	return &idx.edges[r.src][src.TileIndices[int(r.ty)*(src.Width/2)+int(r.tx)]]
}

func (idx *tileIndex) hash(r tileRef) uint64 {
	src := &idx.sources[r.src]
	return idx.hashes[r.src][src.TileIndices[int(r.ty)*(src.Width/2)+int(r.tx)]]
}

// Theme sizes: a map is painted from one source map's art and up to two
// others of like colour, so it looks like one authored world rather than a
// quilt of every map's style; the other sources fill only what the theme
// cannot show.
const (
	themeExtra     = 2
	themeCoverage  = 900 // per mille of the best source's coverage, for the main source
	extraCoverage  = 750 // the same for the others
	themeColourGap = 28 * 28 * 3
)

// chooseTheme picks the theme's sources: the main one at random among those
// whose tiles show nearly as much of the wanted ground as the best one, then
// the nearest in colour among those covering enough.
func (idx *tileIndex) chooseTheme(want []signature, r *stream) []int32 {
	cover := make([]int, len(idx.sources))
	best := 0
	for s := range idx.sources {
		for _, sig := range want {
			if len(idx.own[s][1][sig&relaxMasks[1]]) > 0 {
				cover[s]++
			}
		}
		best = max(best, cover[s])
	}
	var mains []int32
	for s, c := range cover {
		if c*1000 >= best*themeCoverage {
			mains = append(mains, int32(s))
		}
	}
	main := mains[r.intn(len(mains))]
	theme := []int32{main}
	for len(theme) < 1+themeExtra {
		pick, pickGap := int32(-1), int64(themeColourGap)
		for s, c := range cover {
			if slices.Contains(theme, int32(s)) || c*1000 < best*extraCoverage {
				continue
			}
			a, b := idx.colour[main], idx.colour[s]
			gap := (a[0]-b[0])*(a[0]-b[0]) + (a[1]-b[1])*(a[1]-b[1]) + (a[2]-b[2])*(a[2]-b[2])
			if gap < pickGap {
				pick, pickGap = int32(s), gap
			}
		}
		if pick < 0 {
			break
		}
		theme = append(theme, pick)
	}
	return theme
}

// Painting weights. Seams are summed squared RGB differences across a
// tile edge; a continuation's edge with the neighbour it continues is the
// source map's own and costs nothing, and each neighbour it continues is
// worth continueBonus more, so patches of authored art grow as far as the
// ground they show allows. A tile whose art is already on the map costs a
// little less, as real maps repeat their tiles. The water cells must match;
// a tile showing another height band, slope or slope direction than the
// ground under it costs per step of difference, so a transition tile wins
// where exact matches would meet at a hard edge. Heights alone decide where
// units go; the art only has to look right. A tile from outside the theme
// costs much more.
const (
	continueBonus = 60000
	reuseBonus    = 3000
	bandCost      = 60000
	slopeCost     = 150000
	facingCost    = 50000
	foreignCost   = 250000
	shoreCost     = 1000000 // per cell, when no tile shows the shore asked for
)

// Samples per position: of the theme's exact matches, of those matching
// water and slope, and of those matching water alone.
var sampled = [len(relaxMasks)]int{64, 0, 16, 16}

// mismatch is what showing a tile of signature have on ground of signature
// want costs, or -1 when their water cells differ.
func mismatch(have, want signature) int {
	if have&0xF != want&0xF {
		return -1
	}
	field := func(sig signature, shift int) int { return int(sig >> shift & 3) }
	abs := func(v int) int { return max(v, -v) }
	return bandCost*abs(field(have, sigLevelShift)-field(want, sigLevelShift)) +
		slopeCost*abs(field(have, sigSlopeShift)-field(want, sigSlopeShift)) +
		facingCost*bits.OnesCount16(uint16((have^want)>>sigDirShift&0xF))
}

// paint lays a source tile on every tile position of the terrain, row by
// row. A position's candidates are the continuations of its four painted
// neighbours (left, up, and the two upper diagonals) in their own source
// maps, when those show the ground this position needs, and a sample of the
// theme's tiles whose signature matches; the cheapest by the weights above
// wins. It returns the tile numbers and the art.
func paint(ctx context.Context, idx *tileIndex, t *terrain, seed uint64) ([]uint16, []byte, error) {
	r := newStream(mix(seed, 60))
	tw, th := t.w/2, t.h/2
	at := func(x, y int) int32 { return t.height[y*t.w+x] }
	b := landBands(len(t.height), func(i int) int32 { return t.height[i] }, t.sea)
	want := make([]signature, tw*th)
	for ty := 0; ty < th; ty++ {
		for tx := 0; tx < tw; tx++ {
			want[ty*tw+tx] = tileSignature(at, t.w, t.h, tx, ty, t.sea, b)
		}
	}
	theme := idx.chooseTheme(want, r)
	inTheme := func(s int32) bool { return slices.Contains(theme, s) }
	chosen := make([]tileRef, tw*th)
	used := map[uint64]bool{}
	type cand struct {
		ref        tileRef
		left, up   bool // continues that neighbour
		continuing int
	}
	var cands []cand
	var pool [][]tileRef
	for ty := 0; ty < th; ty++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		for tx := 0; tx < tw; tx++ {
			w := want[ty*tw+tx]
			cands = cands[:0]
			var left, up *tileRef
			if tx > 0 {
				left = &chosen[ty*tw+tx-1]
			}
			if ty > 0 {
				up = &chosen[(ty-1)*tw+tx]
			}
			addCont := func(c tileRef, fromLeft, fromUp bool) {
				if !idx.valid(c) || mismatch(idx.sig(c), w) < 0 {
					return
				}
				for i := range cands {
					if cands[i].ref == c {
						cands[i].left, cands[i].up = cands[i].left || fromLeft, cands[i].up || fromUp
						cands[i].continuing++
						return
					}
				}
				cands = append(cands, cand{ref: c, left: fromLeft, up: fromUp, continuing: 1})
			}
			if left != nil {
				addCont(tileRef{left.src, left.tx + 1, left.ty}, true, false)
			}
			if up != nil {
				addCont(tileRef{up.src, up.tx, up.ty + 1}, false, true)
			}
			if tx > 0 && ty > 0 {
				ul := chosen[(ty-1)*tw+tx-1]
				addCont(tileRef{ul.src, ul.tx + 1, ul.ty + 1}, false, false)
			}
			if tx+1 < tw && ty > 0 {
				ur := chosen[(ty-1)*tw+tx+1]
				addCont(tileRef{ur.src, ur.tx - 1, ur.ty + 1}, false, false)
			}
			// Sample the theme; when it has nothing for these water cells,
			// sample every source.
			for l, mask := range relaxMasks {
				for k := 0; k < sampled[l]; k++ {
					pool = pool[:0]
					for _, s := range theme {
						if bucket := idx.own[s][l][w&mask]; len(bucket) > 0 {
							pool = append(pool, bucket)
						}
					}
					if len(pool) == 0 {
						break
					}
					bucket := pool[k%len(pool)]
					cands = append(cands, cand{ref: bucket[r.intn(len(bucket))]})
				}
			}
			if len(cands) == 0 {
				for l, mask := range relaxMasks {
					if bucket := idx.all[l][w&mask]; len(bucket) > 0 {
						for k := 0; k < sampled[0]; k++ {
							cands = append(cands, cand{ref: bucket[r.intn(len(bucket))]})
						}
						break
					}
				}
			}
			// No source shows this shore: take the shore nearest it, by
			// the fewest cells shown wrongly.
			for d := 1; d <= 4 && len(cands) == 0; d++ {
				for alt := signature(0); alt < 16; alt++ {
					if bits.OnesCount16(uint16(alt^w&0xF)) != d {
						continue
					}
					if bucket := idx.all[len(relaxMasks)-1][alt]; len(bucket) > 0 {
						for k := 0; k < sampled[0]/4; k++ {
							cands = append(cands, cand{ref: bucket[r.intn(len(bucket))]})
						}
					}
				}
			}
			if len(cands) == 0 {
				return nil, nil, fmt.Errorf("mapgen: the world's maps have no tiles")
			}
			best, bestCost := cands[0].ref, math.MaxInt
			for _, c := range cands {
				cost := -continueBonus * c.continuing
				e := idx.edgesOf(c.ref)
				if left != nil && !c.left {
					cost += seam(&idx.edgesOf(*left)[edgeRight], &e[edgeLeft])
				}
				if up != nil && !c.up {
					cost += seam(&idx.edgesOf(*up)[edgeBottom], &e[edgeTop])
				}
				if mm := mismatch(idx.sig(c.ref), w); mm >= 0 {
					cost += mm
				} else {
					cost += shoreCost * bits.OnesCount16(uint16((idx.sig(c.ref)^w)&0xF))
				}
				if !inTheme(c.ref.src) {
					cost += foreignCost
				}
				if used[idx.hash(c.ref)] {
					cost -= reuseBonus
				}
				if cost < bestCost {
					best, bestCost = c.ref, cost
				}
			}
			chosen[ty*tw+tx] = best
			used[idx.hash(best)] = true
		}
	}
	// One copy of every distinct tile, numbered in order of first use.
	numbers := map[uint64]uint16{}
	var graphics []byte
	indices := make([]uint16, tw*th)
	for i, c := range chosen {
		h := idx.hash(c)
		n, ok := numbers[h]
		if ok && !bytes.Equal(graphics[int(n)*1024:int(n+1)*1024], idx.art(c)) {
			ok = false // a hash collision: keep both
		}
		if !ok {
			if len(graphics)/1024 >= 1<<16 {
				return nil, nil, fmt.Errorf("mapgen: the map needs more than 65536 distinct tiles")
			}
			n = uint16(len(graphics) / 1024)
			numbers[h] = n
			graphics = append(graphics, idx.art(c)...)
		}
		indices[i] = n
	}
	return indices, graphics, nil
}

// minimap is the 252x252 picture the map chooser and the radar show: the
// play area (the map less its right and bottom insets, PlayRight by
// PlayBottom) fitted by its long side into the top-left of the bitmap, and
// the rest in retail's pad index 0x64 [fmt tnt] "Minimap".
func minimap(w, h int, indices []uint16, graphics []byte) (int, int, []byte) {
	const side, pad = 252, 0x64
	playW, playH := w*16-32, h*16-128
	usedW, usedH := side, side
	if playW < playH {
		usedW = playW * side / playH
	} else {
		usedH = playH * side / playW
	}
	out := make([]byte, side*side)
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			if x >= usedW || y >= usedH {
				out[y*side+x] = pad
				continue
			}
			wx, wy := x*playW/usedW, y*playH/usedH
			n := int(indices[(wy/32)*(w/2)+wx/32])
			out[y*side+x] = graphics[n*1024+(wy%32)*32+wx%32]
		}
	}
	return side, side, out
}

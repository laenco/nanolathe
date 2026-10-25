package mapgen

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"

	"github.com/nanolathe-gg/nanolathe/formats"
)

// Candidates are the layouts made and measured per round; the fairest one
// is painted. Rounds repeat, up to maxRounds, while none of a round's
// candidates connects every player.
const (
	Candidates = 8
	maxRounds  = 3
)

// Widening short bases: at most roomRounds rounds of roomStep more cells of
// radius, until the room's spread is within roomTolerance per mille.
const (
	roomRounds    = 4
	roomStep      = 3
	roomTolerance = 40
)

// Generator makes maps for one world. Building it indexes the world's tile
// art once, so a player trying seed after seed waits only for the maps.
type Generator struct {
	// Workers bounds how many candidates are built at once; 0 uses
	// min(Candidates, GOMAXPROCS). A host keeping the game responsive while
	// a preview generates sets it lower. The map is the same for any value.
	Workers int

	world World
	index *tileIndex
	wet   bool
}

// NewGenerator checks a world and indexes its source maps' tiles.
func NewGenerator(w World) (*Generator, error) {
	switch {
	case len(w.Sources) == 0:
		return nil, errors.New("mapgen: the world has no maps to take tiles from")
	case w.Palette == nil:
		return nil, errors.New("mapgen: the world has no palette")
	case len(w.Classes) == 0:
		return nil, errors.New("mapgen: the world has no movement class to measure routes with")
	case w.Deposit.Name != "" && w.Deposit.Metal <= 0:
		return nil, errors.New("mapgen: the world's metal deposit holds no metal")
	}
	idx, err := buildIndex(w.Sources, w.Palette)
	if err != nil {
		return nil, err
	}
	g := &Generator{world: w, index: idx}
	for l := range idx.all[len(relaxMasks)-1] {
		if l&0xF != 0 {
			g.wet = true
			break
		}
	}
	return g, nil
}

// Wet reports whether the world's maps have water to paint; a world without
// any makes dry maps only.
func (g *Generator) Wet() bool { return g.wet }

// Start is a start position in map pixels, as an OTA's specials carry it.
type Start struct{ X, Z int }

// Map is a generated map.
type Map struct {
	// Code makes this map again with ParseCode and Generate.
	Code    string
	Params  Params
	Balance Balance
	// Width and Height are in 16-pixel cells.
	Width, Height int
	Starts        []Start
	Deposits      int
	// TNT is the terrain file; Minimap is its picture, also inside it.
	TNT                         []byte
	MinimapWidth, MinimapHeight int
	Minimap                     []byte

	world World
}

// Progress reports finished steps out of all of them.
type Progress func(done, total int)

// candidate is one measured layout; why says why it is not valid.
type candidate struct {
	ok       bool
	why      reject
	balance  Balance
	terrain  *terrain
	features *features
	starts   []point
}

// Generate makes the map the parameters name. Candidates are built at most
// Workers at a time, each from its own seed, and the fairest valid one is
// kept, so the result does not depend on how many run at once.
//
// A cancelled context stops the work between steps, and Generate then
// returns the context's error. A cancel that lands after the last check
// still returns the finished map, so a caller that must not show a stale
// one compares the request it receives with the one it is waiting for.
func (g *Generator) Generate(ctx context.Context, p Params, progress Progress) (*Map, error) {
	p = p.Normalize()
	if p.Water > 0 && !g.wet {
		return nil, fmt.Errorf("mapgen: %s's maps have no water to paint", g.world.Name)
	}
	w, h := p.Size*cellsPerUnit+voidRight, p.Size*cellsPerUnit+voidBottom
	total := Candidates*maxRounds + 1
	done := 0
	report := func() {
		done++
		if progress != nil {
			progress(done, total)
		}
	}
	workers := g.Workers
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	workers = min(workers, Candidates)
	var best *candidate
	var rejected [rejectKinds]int
	for round := 0; round < maxRounds && best == nil; round++ {
		found := make([]candidate, Candidates)
		var wg sync.WaitGroup
		jobs := make(chan int)
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for k := range jobs {
					if ctx.Err() == nil {
						found[k] = g.candidate(ctx, p, w, h, mix(p.Seed, uint64(100+round*Candidates+k)))
					}
				}
			}()
		}
		for k := range found {
			jobs <- k
		}
		close(jobs)
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for k := range found {
			report()
			if !found[k].ok {
				rejected[found[k].why]++
				continue
			}
			if best == nil || found[k].balance.Score < best.balance.Score {
				best = &found[k]
			}
		}
	}
	if best == nil {
		return nil, fmt.Errorf("mapgen: no layout of %d players on a %dx%d map %s; try another seed, less relief or a larger map", p.Players, p.Size, p.Size, mostRejected(rejected).failure())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m, err := g.finish(ctx, p, best)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	done = total - 1
	report()
	return m, nil
}

// reject is why a candidate is not valid. Validity comes before ranking: a
// layout is ranked only when its bases stand apart, every start reaches every
// enemy start by land, every player's metal is placed and every start is
// usable (startUsable).
type reject uint8

const (
	rejectCrowded     reject = iota // the bases would overlap
	rejectUnconnected               // a start cannot reach an enemy start
	rejectNoMetal                   // the deposits could not be placed fairly
	rejectUnusable                  // a start is wet or too cramped to build on
	rejectKinds
)

// failure completes "no layout of ... on a map" for an error.
func (r reject) failure() string {
	switch r {
	case rejectCrowded:
		return "kept every base apart"
	case rejectNoMetal:
		return "placed every player's metal"
	case rejectUnusable:
		return "gave every start dry, level room to build"
	}
	return "connected every base"
}

// mostRejected is the most common reason, the earliest on a tie.
func mostRejected(n [rejectKinds]int) reject {
	most := rejectCrowded
	for r := rejectCrowded; r < rejectKinds; r++ {
		if n[r] > n[most] {
			most = r
		}
	}
	return most
}

// minRoom is the least building room (Balance.Room) a usable start has:
// about half the area of the flat base every start is given.
const minRoom = baseRadius * baseRadius * 3 / 2

// startUsable reports whether a start stands on dry land and has room to
// build a base.
func startUsable(t *terrain, s point, room int64) bool {
	return t.height[s.y*t.w+s.x] > t.sea && room >= minRoom
}

// candidate builds and measures one layout from its seed.
func (g *Generator) candidate(ctx context.Context, p Params, w, h int, seed uint64) candidate {
	r := newStream(mix(seed, 10))
	starts := placeStarts(p, w, h, r)
	for i := range starts {
		for j := i + 1; j < len(starts); j++ {
			if dx, dy := starts[i].x-starts[j].x, starts[i].y-starts[j].y; dx*dx+dy*dy < (2*baseRadius+4)*(2*baseRadius+4) {
				return candidate{why: rejectCrowded} // too many players for the map
			}
		}
	}
	t := makeTerrain(p, w, h, mix(seed, 20), starts)
	if ctx.Err() != nil {
		return candidate{}
	}
	f := newFeatures(w, h)
	// Trees go first, so the travel costs the metal is placed by already
	// route round them.
	placeVegetation(g.world, t, f, starts, mix(seed, 30))
	mc := g.world.Classes[0]
	tiers := footprintTiers(cellTiers(t.height, w, h, t.sea, g.world.Lava, f.blocking, mc), w, h, mc)
	anchors := make([]int, len(starts))
	for i, s := range starts {
		anchors[i] = anchorOf(s, w, mc)
	}
	_, owner := travel(tiers, w, h, anchors)
	dist := make([][]int32, len(starts))
	reach := int32(-1)
	for i := range starts {
		dist[i], _ = travel(tiers, w, h, anchors[i:i+1])
		for j := range starts {
			if teamOf(p, j) == teamOf(p, i) {
				continue
			}
			d := dist[i][anchors[j]]
			if d == unreached {
				return candidate{why: rejectUnconnected}
			}
			if reach < 0 || d/2 < reach {
				reach = d / 2
			}
		}
	}
	if ctx.Err() != nil {
		return candidate{}
	}
	if !placeDeposits(p, g.world, t, f, starts, dist, owner, reach, r) {
		return candidate{why: rejectNoMetal}
	}
	b, ok := measure(p, g.world, t, f, starts, reach)
	// A base short of building room is widened, a few cells a round,
	// until every base has nearly the most any has.
	for round := 1; ok && round <= roomRounds && b.Room.Spread > roomTolerance && ctx.Err() == nil; round++ {
		room := b.Room.Values
		most := slices.Max(room)
		for i, s := range starts {
			if room[i]*1000 < most*(1000-roomTolerance) {
				t.widenBase(s, baseRadius+roomStep*round)
			}
		}
		// A deposit in a widened base's blend ring is levelled again, so
		// its extractor still stands on flat ground.
		for _, pl := range f.list {
			if g.world.Deposit.Name != "" && pl.feature.Name == g.world.Deposit.Name {
				t.levelPad(pl.x, pl.y, max(1, pl.feature.FootX), max(1, pl.feature.FootZ))
			}
		}
		b, ok = measure(p, g.world, t, f, starts, reach)
	}
	if !ok {
		return candidate{why: rejectUnconnected}
	}
	for i, s := range starts {
		if !startUsable(t, s, b.Room.Values[i]) {
			return candidate{why: rejectUnusable}
		}
	}
	return candidate{ok: true, balance: b, terrain: t, features: f, starts: starts}
}

// finish paints the chosen candidate and encodes its terrain file.
func (g *Generator) finish(ctx context.Context, p Params, c *candidate) (*Map, error) {
	t, f := c.terrain, c.features
	indices, graphics, err := paint(ctx, g.index, t, mix(p.Seed, 50))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sortPlaced(f.list)
	heights := make([]byte, len(t.height))
	for i, v := range t.height {
		heights[i] = byte(v)
	}
	cells := make([]uint16, t.w*t.h)
	for i := range cells {
		cells[i] = 0xFFFF
	}
	var names []string
	number := map[string]uint16{}
	deposits := 0
	for _, pl := range f.list {
		n, ok := number[pl.feature.Name]
		if !ok {
			n = uint16(len(names))
			number[pl.feature.Name] = n
			names = append(names, pl.feature.Name)
		}
		cells[pl.y*t.w+pl.x] = n
		if g.world.Deposit.Name != "" && pl.feature.Name == g.world.Deposit.Name {
			deposits++
		}
	}
	// The rest of each footprint is fringe, as retail's files author it
	// [fmt tnt] "Attribute map".
	for _, pl := range f.list {
		for y := pl.y; y < min(t.h, pl.y+pl.feature.FootZ); y++ {
			for x := pl.x; x < min(t.w, pl.x+pl.feature.FootX); x++ {
				if cells[y*t.w+x] == 0xFFFF {
					cells[y*t.w+x] = 0xFFFE
				}
			}
		}
	}
	mw, mh, mini := minimap(t.w, t.h, indices, graphics)
	tnt, err := formats.EncodeTNT(formats.TNTWrite{
		Width: t.w, Height: t.h, SeaLevel: byte(t.sea),
		TileIndices: indices, Heights: heights, Features: cells,
		TileGraphics: graphics, FeatureNames: names,
		MinimapWidth: mw, MinimapHeight: mh, Minimap: mini,
	})
	if err != nil {
		return nil, err
	}
	m := &Map{
		Code: Code(g.world.Name, p), Params: p, Balance: c.balance,
		Width: t.w, Height: t.h, Deposits: deposits,
		TNT: tnt, MinimapWidth: mw, MinimapHeight: mh, Minimap: mini,
		world: g.world,
	}
	// The map keeps the world's name and climate for its OTA, not the
	// source maps' art.
	m.world.Sources = nil
	for _, s := range c.starts {
		m.Starts = append(m.Starts, Start{X: s.x*16 + 8, Z: s.y*16 + 8})
	}
	return m, nil
}

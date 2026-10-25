package aikit

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/ai"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"github.com/nanolathe-gg/nanolathe/internal/world"
)

// wallTerrain is a 64-cell square map whose feature definitions are a
// two-cell wall piece (0) and a one-cell tree (1), with anchors at the
// given cells.
func wallTerrain(walls, trees [][2]int32) (*world.Terrain, *content.FeatureDef, *Table) {
	wall := &content.FeatureDef{Blocking: true, Reclaimable: true, FootprintX: 2, FootprintZ: 2, Metal: 8}
	tree := &content.FeatureDef{Blocking: true, Reclaimable: true, FootprintX: 1, FootprintZ: 1}
	ter := &world.Terrain{CellW: 64, CellH: 64, Plot: make([]world.PlotCell, 64*64), FeatureDefs: []*content.FeatureDef{wall, tree}}
	for i := range ter.Plot {
		ter.Plot[i].SetFeature(world.PlotFeatureNone)
	}
	for _, c := range walls {
		ter.PlotAt(c[0], c[1]).SetFeature(0)
	}
	for _, c := range trees {
		ter.PlotAt(c[0], c[1]).SetFeature(1)
	}
	return ter, wall, &Table{defensiveFeatures: map[*content.FeatureDef]bool{wall: true}}
}

// The wall list holds the wall pieces within WallRadius of the points the
// think watched, each once however many points see it, at their footprint
// centres; trees are not walls, and a think that watches nothing lists
// none.
func TestWallsListedNearWatchedPoints(t *testing.T) {
	ter, _, tab := wallTerrain([][2]int32{{10, 10}, {40, 40}}, [][2]int32{{12, 10}})
	obs := &Obs{}
	e := &executor{m: &ai.Manager{Terrain: ter}, mapInfo: &MapInfo{CellW: 64, CellH: 64}, obs: obs, table: tab}
	b := &batch{}
	b.watch[0], b.watch[1], b.nWatch = [2]int32{180, 180}, [2]int32{180, 220}, 2
	e.refreshWalls(b, 90)
	if len(obs.Walls) != 1 || obs.Walls[0] != (Wall{X: 176, Z: 176, FootX: 2, FootZ: 2, Reclaimable: true}) || obs.WallsTick != 90 {
		t.Fatalf("walls %+v at %d, want the one piece at (176,176) at 90", obs.Walls, obs.WallsTick)
	}
	b.reset()
	e.refreshWalls(b, 120)
	if len(obs.Walls) != 0 {
		t.Fatalf("an unwatched think listed %+v", obs.Walls)
	}
}

// A breach reclaims the wall pieces within its radius, nearest first, with
// ordinary Reclaim orders that replace the builder's own; with only a tree
// there it fails and leaves the builder's orders alone.
func TestBreachReclaimsTheWallPiecesThere(t *testing.T) {
	for _, tc := range []struct {
		name   string
		x, z   int32
		orders int
	}{
		{"a wall line", 176, 208, 3},
		{"only a tree", 520, 520, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGenFixture(t, &countBrain{})
			f.def.CanReclamate = true
			ter, _, tab := wallTerrain([][2]int32{{10, 10}, {10, 12}, {10, 14}, {10, 30}}, [][2]int32{{32, 32}})
			e := &executor{m: &ai.Manager{Terrain: ter}, mapInfo: &MapInfo{CellW: 64, CellH: 64}, obs: &Obs{}, table: tab}
			u := f.w.Unit(f.own)
			b := &batch{actors: []pool.Handle{f.own}, inst: []*units.Unit{u}}
			c := &Command{Kind: CmdBreach, first: 0, count: 1, X: tc.x, Z: tc.z, Count: 48}
			if got := e.execBreach(c, b, 30, f.w); got != (tc.orders > 0) {
				t.Fatalf("issued=%v", got)
			}
			q := orders.QueueOfUnit(u)
			n := 0
			if q != nil {
				n = q.LenPrimary()
			}
			if n != tc.orders {
				t.Fatalf("%d orders, want %d", n, tc.orders)
			}
			if tc.orders == 0 {
				return
			}
			head := q.Head()
			if orders.Table()[head.ID].Name != "Reclaim" || int32(int64(head.GoalX)>>16) != 10*16+8 || int32(int64(head.GoalZ)>>16) != 12*16+8 {
				t.Fatalf("head %s at (%d,%d), want Reclaim at the middle piece's anchor", orders.Table()[head.ID].Name, int64(head.GoalX)>>16, int64(head.GoalZ)>>16)
			}
		})
	}
}

// A wall piece is an unarmed, immobile land product that finishes as a
// solid feature; armed, mobile, economy and water products are not.
func TestWallPieceReadsAuthoredKeys(t *testing.T) {
	solid := &content.FeatureDef{Blocking: true, FootprintX: 2, FootprintZ: 2, Damage: 4000}
	piece := func() *UnitInfo {
		return &UnitInfo{Def: &content.UnitDef{IsFeature: true}, FinishedFeature: solid}
	}
	if !WallPiece(piece()) {
		t.Fatal("a dragon's tooth is not a wall piece")
	}
	for name, change := range map[string]func(*UnitInfo){
		"armed":       func(u *UnitInfo) { u.DPS = 10 },
		"mobile":      func(u *UnitInfo) { u.Role |= RoleMobile },
		"energy":      func(u *UnitInfo) { u.Role |= RoleEnergy },
		"in water":    func(u *UnitInfo) { u.Def.MinWaterDepth = 1 },
		"not feature": func(u *UnitInfo) { u.Def.IsFeature = false },
		"no feature":  func(u *UnitInfo) { u.FinishedFeature = nil },
		"walkable":    func(u *UnitInfo) { u.FinishedFeature = &content.FeatureDef{FootprintX: 2, FootprintZ: 2, Damage: 4000} },
	} {
		u := piece()
		change(u)
		if WallPiece(u) {
			t.Errorf("%s: counted as a wall piece", name)
		}
	}
}

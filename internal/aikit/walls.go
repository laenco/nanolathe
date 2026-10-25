package aikit

import (
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// Walls: the finished features of wall products (dragon's teeth,
// fortification walls; Table.defensiveFeatures) where the brain looks for
// them. A map feature has no owner, so the brain decides whose a piece is
// from where it stands (docs/DESIGN_SESSIONS_AI_SAVE.md "Modern AI walls").

// WallPiece reports whether a product is a wall piece: an unarmed,
// immobile land product that finishes as a solid feature with hit points
// (or none to lose) and is no economy, factory or sensor building. It reads
// authored keys only, never a unit name.
func WallPiece(u *UnitInfo) bool {
	if u == nil || u.Def == nil || u.Role.Has(RoleMobile) || u.Def.BMCode != 0 {
		return false
	}
	f := u.FinishedFeature
	return u.Def.IsFeature && u.DPS == 0 && u.Def.MinWaterDepth <= 0 && f != nil && f.Blocking && f.FootprintX > 0 && f.FootprintZ > 0 && (f.Damage > 0 || f.Indestructible) &&
		!u.Role.Any(RoleEnergy|RoleExtractor|RoleMetalMaker|RoleStorage|RoleFactory|RoleRadar|RoleSonar|RoleJammer)
}

// WatchWalls asks for the wall pieces within WallRadius of (x, z) to be
// listed in Obs.Walls when this think's batch is applied, for the next
// think. The brain names points where its own units stand, so the pieces
// listed are ones those units see. Up to maxWallWatch points a think are
// kept; a think that names none leaves the list empty.
func (k *Kit) WatchWalls(x, z int32) {
	b := k.out
	if b == nil || b.nWatch >= len(b.watch) {
		return
	}
	b.watch[b.nWatch] = [2]int32{x, z}
	b.nWatch++
}

// Breach orders a builder to reclaim the wall pieces within radius of
// (x, z), nearest first, up to four: the opening a held army needs. With
// no wall piece there it fails and the builder keeps its orders.
func (k *Kit) Breach(builder pool.Handle, x, z, radius int32) {
	var one [1]pool.Handle
	one[0] = builder
	k.push(Command{Kind: CmdBreach, X: x, Z: z, Count: radius, Spot: -1}, one[:])
}

// refreshWalls lists the wall pieces near the applied batch's watch points
// into the observation, each piece once, in watch-point and row order. It
// runs on the simulation thread when the batch is applied.
func (e *executor) refreshWalls(b *batch, tick uint32) {
	o, m, t := e.obs, e.mapInfo, e.m.Terrain
	if o == nil {
		return
	}
	o.Walls = o.Walls[:0]
	o.WallsTick = tick
	if m == nil || t == nil || e.table == nil || len(e.table.defensiveFeatures) == 0 {
		return
	}
	r := int32(WallRadius)
	lim := int64(r) * int64(r)
	for i := 0; i < b.nWatch; i++ {
		px, pz := b.watch[i][0], b.watch[i][1]
		cx0, cz0 := max((px-r)/16, 0), max((pz-r)/16, 0)
		cx1, cz1 := min((px+r)/16, m.CellW-1), min((pz+r)/16, m.CellH-1)
		for cz := cz0; cz <= cz1; cz++ {
			for cx := cx0; cx <= cx1; cx++ {
				cell := t.PlotAt(cx, cz)
				if cell == nil || !cell.IsRealFeature() {
					continue // anchor cells only: each piece once per point
				}
				def, ok := t.FeatureDefAt(cell.Feature())
				if !ok || def == nil || !e.table.defensiveFeatures[def] {
					continue
				}
				fx, fz := max(def.FootprintX, 1), max(def.FootprintZ, 1)
				x, z := cx*16+fx*8, cz*16+fz*8
				if Dist2(x, z, px, pz) > lim || listedWall(o.Walls, x, z) {
					continue
				}
				if len(o.Walls) >= maxWalls {
					return
				}
				o.Walls = append(o.Walls, Wall{X: x, Z: z, FootX: fx, FootZ: fz, Reclaimable: def.Reclaimable && !def.Indestructible})
			}
		}
	}
}

func listedWall(ws []Wall, x, z int32) bool {
	for i := range ws {
		if ws[i].X == x && ws[i].Z == z {
			return true
		}
	}
	return false
}

// execBreach applies a Breach: reclaim orders on the reclaimable wall
// pieces nearest the point, the first replacing the builder's orders.
func (e *executor) execBreach(c *Command, b *batch, tick uint32, w *units.World) bool {
	m := e.mapInfo
	t := e.m.Terrain
	u := e.actorOK(w, b.actors[c.first], b.inst[c.first])
	if u == nil {
		e.stats.Stale++
		e.stats.Reasons[FailNoActor]++
		e.checkpointReject()
		return false
	}
	if m == nil || t == nil || e.table == nil || u.Def == nil || !u.Def.CanReclamate {
		e.stats.Failed++
		e.stats.Reasons[FailResolve]++
		e.stats.noteResolve(c.Kind, u)
		e.checkpointReject()
		return false
	}
	radius := max(c.Count, 32)
	lim := int64(radius) * int64(radius)
	cx0, cz0 := max((c.X-radius)/16, 0), max((c.Z-radius)/16, 0)
	cx1, cz1 := min((c.X+radius)/16, m.CellW-1), min((c.Z+radius)/16, m.CellH-1)
	var picks [4]clearPick
	n := 0
	for cz := cz0; cz <= cz1; cz++ {
		for cx := cx0; cx <= cx1; cx++ {
			cell := t.PlotAt(cx, cz)
			if cell == nil || !cell.IsRealFeature() {
				continue
			}
			def, ok := t.FeatureDefAt(cell.Feature())
			if !ok || def == nil || !e.table.defensiveFeatures[def] || !def.Reclaimable || def.Indestructible {
				continue
			}
			fx, fz := max(def.FootprintX, 1), max(def.FootprintZ, 1)
			d := Dist2(cx*16+fx*8, cz*16+fz*8, c.X, c.Z)
			if d > lim {
				continue
			}
			// Nearest first; row order breaks ties (insertion keeps the
			// earlier of equal distances first).
			sc := -d
			pos := n
			for pos > 0 && picks[pos-1].score < sc {
				pos--
			}
			if pos >= len(picks) {
				continue
			}
			end := min(n, len(picks)-1)
			for j := end; j > pos; j-- {
				picks[j] = picks[j-1]
			}
			picks[pos] = clearPick{cx: cx, cz: cz, score: sc, metal: def.Metal}
			if n < len(picks) {
				n++
			}
		}
	}
	if n == 0 {
		e.stats.Failed++
		e.stats.Reasons[FailNoSite]++
		e.checkpointReject()
		return false
	}
	q := orders.BindQueueBinding(u, e.m.OrderBinding)
	if q == nil {
		e.stats.Failed++
		e.checkpointReject()
		return false
	}
	issued := false
	for i := 0; i < n; i++ {
		if e.reclaimFeature(u, q, picks[i].cx, picks[i].cz, tick, !issued) {
			issued = true
		}
	}
	if !issued {
		e.stats.Failed++
		e.stats.Reasons[FailResolve]++
		e.stats.noteResolve(c.Kind, u)
		e.checkpointReject()
		return false
	}
	e.stats.Breaches++
	return true
}

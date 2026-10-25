package utility

import (
	"github.com/nanolathe-gg/nanolathe/internal/aikit"
	"github.com/nanolathe-gg/nanolathe/internal/aikit/core"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
)

// Wall segments (wall_build=1, with the defense plan). From minute
// wall_from, a short segment of wall pieces goes in front of a completed
// tower that serves the ground plan (a ground tower, or a missile tower
// under the front rules) in a zone enemy ground forces have approached: wallPieces
// touching pieces across the line from the tower toward the zone's observed
// approach (defPlan.approach), wallAhead in front of it. A small-bore shell
// fired at the tower from beyond bursts on the pieces and harms neither
// [06 §9.3][06 §13.1], and attackers walk around. Towers stand at least
// defSpace apart, so the segments of neighbouring towers leave a corridor
// between them and our own army is never shut in. A piece keeps off the
// corridors our units keep walking (defPlan.busy) and out of passes
// (defPlan.inNeck), and Kit.BuildExact refuses a blocked site, a factory
// lane or a metal spot rather than move the piece. Only a constructor with
// nothing better to do builds one — idle, or helping another build — the
// nearest to the segment, one segment every wallEvery, one per tower, and
// only while the stores, or wallIncome of income, pay for it. A think whose
// action budget cannot carry the whole segment orders what it can and
// queues the rest on the same constructor on later thinks, while it keeps
// the job (docs/DESIGN_SESSIONS_AI_SAVE.md "Modern AI walls").
const (
	wallPieces = 3
	wallAhead  = 96   // world units from the tower's centre to the segment's
	wallEvery  = 2700 // ticks between segments
	wallIncome = 90   // seconds of income that may pay for a segment
	// wallSeen is the approach weight (strength-ticks, defPlan.atkW) a
	// zone must have seen: a few units' worth of sightings.
	wallSeen = 2000 * 30
)

// wallWhyNames name wallState.why's reasons: no tower owed a segment in
// an approached zone, no free constructor that makes an affordable piece,
// no room for two pieces.
var wallWhyNames = [...]string{"tower", "builder", "site"}

// wallState is the economy's record of the segments it ordered.
type wallState struct {
	towers   []wallTower // by handle: the tower a segment was ordered for
	last     uint32      // tick of the last segment
	segments int64
	pieces   int64
	// why counts the thinks a segment was due but not ordered, by reason
	// (wallWhyNames; Report only).
	why [3]int64
	// The segment being ordered: its piece sites, how many were ordered,
	// and the constructor, its instance and the piece it builds.
	pts        [wallPieces][2]int32
	n, ordered int
	who        pool.Handle
	gen        uint32
	prod       *aikit.UnitInfo
}

// wallTower names the tower instance that has its segment.
type wallTower struct {
	def *aikit.UnitInfo
	gen uint32
}

// walls orders a wall segment when one is owed; it returns the actions
// used.
func (e *Economy) walls(b *core.Board, budget int) int {
	s := e.s
	if budget <= 0 {
		return 0
	}
	if e.wl.ordered < e.wl.n {
		return e.wallMore(b, budget)
	}
	if s.p.WallBuild == 0 || s.p.DefPlan == 0 || s.tick < uint32(s.p.WallFrom)*1800 ||
		(e.wl.last != 0 && s.tick-e.wl.last < wallEvery) {
		return 0
	}
	pl := e.plan()
	o := b.O
	m := b.K.Map
	// The tower owed a segment nearest the enemy.
	tower := int32(-1)
	var towerD int64
	for _, i := range b.Defenses {
		u := &o.Own[i]
		if !u.Built || !pl.groundPlan(u.Info) || e.wl.has(u) {
			continue
		}
		if pl.atkW[pl.zoneOf(u.X, u.Z)] < wallSeen {
			continue
		}
		if d := aikit.Dist2(u.X, u.Z, s.enemyX, s.enemyZ); tower < 0 || d < towerD {
			tower, towerD = i, d
		}
	}
	if tower < 0 {
		e.wl.why[0]++
		return 0
	}
	t := &o.Own[tower]
	// The free constructor nearest the tower that makes a piece the stores
	// pay for.
	pick := int32(-1)
	var piece *aikit.UnitInfo
	var pickD int64
	for _, bi := range b.Builders {
		u := &o.Own[bi]
		if u.Info.Role.Has(aikit.RoleCommander) {
			continue
		}
		if c := s.commitOf(u); u.Order != aikit.OrderIdle && c.kind != cAssist && c.kind != cGuard {
			continue
		}
		p := e.wallPiece(b, u.Info)
		if p == nil {
			continue
		}
		if d := aikit.Dist2(u.X, u.Z, t.X, t.Z); pick < 0 || d < pickD {
			pick, pickD, piece = bi, d, p
		}
	}
	if pick < 0 {
		e.wl.why[1]++
		return 0
	}
	// The segment: across the approach, wallAhead in front of the tower,
	// on the construction grid the executor uses.
	dx, dz := pl.approach(s, t.X, t.Z, pl.zoneOf(t.X, t.Z), dcGround)
	fx := max(piece.FootX, piece.FinishedFeature.FootprintX)
	fz := max(piece.FootZ, piece.FinishedFeature.FootprintZ)
	bx, bz := max(piece.FootX, 1), max(piece.FootZ, 1)
	// Neighbours a piece's width apart along the larger axis of the line,
	// so pieces on a diagonal touch without overlapping once each is put on
	// the grid.
	spacing := (int64(max(fx, fz))*16*one + max(abs64(dx), abs64(dz)) - 1) / max(abs64(dx), abs64(dz), 1)
	cx := int64(t.X) + dx*wallAhead/one
	cz := int64(t.Z) + dz*wallAhead/one
	limit := pl.flowLimit(m, t.X, t.Z)
	n := 0
	for k := int64(0); k < wallPieces; k++ {
		lat := (k - (wallPieces-1)/2) * spacing
		x := int32(cx - dz*lat/one)
		z := int32(cz + dx*lat/one)
		x = (x/16-bx/2)*16 + bx*8
		z = (z/16-bz/2)*16 + bz*8
		if x < 64 || z < 64 || x > m.WorldW-64 || z > m.WorldH-64 || pl.busy(m, x, z, limit) || pl.inNeck(x, z) {
			continue
		}
		e.wl.pts[n] = [2]int32{x, z}
		n++
	}
	e.wl.mark(t, s.hcap) // a tower whose segment cannot stand is not tried again
	if n < 2 {
		e.wl.why[2]++
		return 0
	}
	u := &o.Own[pick]
	e.wl.n, e.wl.ordered, e.wl.who, e.wl.gen, e.wl.prod = n, 0, u.H, u.Gen, piece
	e.wl.last = s.tick
	e.wl.segments++
	*s.commitOf(u) = commitment{def: u.Info, gen: u.Gen, kind: cWall, prod: piece, spot: -1, x: int32(cx), z: int32(cz), tick: s.tick}
	d := e.record(u)
	d.note = noteAssign
	d.top[0] = cand{kind: cWall, prod: piece, x: int32(cx), z: int32(cz), score: 1, f: [4]int64{int64(n), towerD, 0, 0}}
	d.n, d.chosen = 1, 0
	return e.wallMore(b, budget)
}

// wallMore orders the segment's next pieces, as many as the budget allows,
// on its constructor while the constructor lives and keeps the job; it
// returns the actions used.
func (e *Economy) wallMore(b *core.Board, budget int) int {
	w := &e.wl
	i := b.Index(w.who)
	if i < 0 || b.O.Own[i].Gen != w.gen || e.s.commitOf(&b.O.Own[i]).kind != cWall {
		w.n = 0
		return 0
	}
	u := &b.O.Own[i]
	used := 0
	for w.ordered < w.n && used < budget {
		pt := w.pts[w.ordered]
		b.K.BuildExact(u.H, w.prod, pt[0], pt[1], w.ordered > 0)
		w.ordered++
		w.pieces++
		used++
	}
	return used
}

// wallPiece is the cheapest wall piece a builder makes that its
// restriction cap allows and the stores, or wallIncome of income, pay a
// whole segment of; nil when none.
func (e *Economy) wallPiece(b *core.Board, builder *aikit.UnitInfo) *aikit.UnitInfo {
	s := e.s
	var best *aikit.UnitInfo
	for _, p := range builder.Builds {
		if !s.info[p.Index].wall || s.k.Allowance(p) <= 0 {
			continue
		}
		if int64(p.Metal)*wallPieces > max(int64(b.Metal.Stock), int64(b.Metal.Income)*wallIncome) ||
			int64(p.Energy)*wallPieces > max(int64(b.Energy.Stock), int64(b.Energy.Income)*wallIncome) {
			continue
		}
		if best == nil || p.Value < best.Value {
			best = p
		}
	}
	return best
}

// has reports whether the tower already has its segment.
func (w *wallState) has(u *aikit.OwnUnit) bool {
	if int(u.H) >= len(w.towers) {
		return false
	}
	t := &w.towers[u.H]
	return t.def == u.Info && t.gen == u.Gen
}

// mark records the tower's segment.
func (w *wallState) mark(u *aikit.OwnUnit, hcap int) {
	w.towers = handleSlots(w.towers, u.H, hcap)
	w.towers[u.H] = wallTower{def: u.Info, gen: u.Gen}
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

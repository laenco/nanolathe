package tactics

import (
	"github.com/nanolathe-gg/nanolathe/internal/aikit"
	"github.com/nanolathe-gg/nanolathe/internal/aikit/core"
)

// Walls (walls=1): an engaged ground squad held at wall pieces breaks
// through them instead of standing at the wall (README "Walls";
// docs/DESIGN_SESSIONS_AI_SAVE.md "Modern AI walls").
//
// An engaged squad watches for wall pieces around its centre and its front
// member, the one nearest the target (Kit.WatchWalls). It is held when the
// members within heldNear of a piece that stands by no own or allied
// building are worth a quarter of its present value and its front has not
// come breachProgress nearer the target for heldTicks (less with skill):
// patrolling members walk up and down the wall, so standing still is no
// test. A held squad is walled. A walled squad:
//
//   - fires at one piece, attacking the ground at its centre with the
//     members whose weapons damage walls (uclass.wallDPS) within their
//     range of it plus breachReach: the piece nearest the members at the
//     wall, priced by the enemy's static fire reaching it, so the breach is
//     made where the towers cover least, and beside the piece that fell
//     last, so a gap widens rather than a second one opening;
//   - sends the members that cannot damage the wall and stand in enemy fire
//     near it back behind the breach until the piece falls: a small-bore
//     shell bursts on the wall's face and harms nothing on either side;
//   - asks the economy for a builder to reclaim the pieces there
//     (Board.BreachX), which takes a builder about a second a piece;
//   - pulls back, cooling the target, once it has lost breachLoss of the
//     strength it had when it was walled, or when breachGiveUp passes
//     without a piece falling.
//
// Once its piece falls the breakers patrol on to the target; a squad still
// held picks the next piece.
const (
	heldNear       = 160
	breachProgress = 96
	breachReach    = 120
	breachOwn      = 320  // a piece this close to an own or allied building is ours
	breachLoss     = 50   // percent of the walled square-law strength lost before pulling back
	holdBack       = 300  // how far behind the piece the members that cannot damage it wait
	breachGiveUp   = 2700 // ticks without a piece falling
	wallLinger     = 300  // ticks a squad stays walled once nothing holds it
)

// heldTicks is how long a squad at a wall must make no progress to count as
// held: 260 ticks at skill 20 (easy), 130 at 85 (hard).
func heldTicks(skill int32) uint32 {
	return uint32(300 - 2*min(max(skill, 0), 100))
}

// walls runs the squad's wall handling during an engagement. It reports
// whether the squad is walled (its breakers carry their own orders) and
// whether it retreated.
func (a *Army) walls(b *core.Board, s *squad) (walled, retreated bool) {
	if !a.P.Walls || s.id == sqNaval {
		return false, false
	}
	o := b.O
	k := b.K
	front := int32(-1)
	var frontD int64
	for _, list := range [2][]int32{s.members, s.stuck} {
		for _, i := range list {
			u := &o.Own[i]
			if d := aikit.Dist2(u.X, u.Z, s.tx, s.tz); front < 0 || d < frontD {
				front, frontD = i, d
			}
		}
	}
	if front < 0 {
		return false, false
	}
	// The piece being broken is watched first, so its absence from a later
	// observation means it fell.
	if s.walled && s.wallPiece {
		k.WatchWalls(s.wallX, s.wallZ)
	}
	f := &o.Own[front]
	k.WatchWalls(s.cx, s.cz)
	if aikit.Dist2(f.X, f.Z, s.cx, s.cz) > 200*200 {
		k.WatchWalls(f.X, f.Z)
	}
	if s.walled && s.wallPiece && o.WallsTick > s.wallAt && !listedWall(o, s.wallX, s.wallZ) {
		s.wallPiece = false
		s.fellX, s.fellZ, s.breachLast = s.wallX, s.wallZ, b.Tick
		a.stats.breached++
	}
	// The pieces that are not ours, then the members at them: their value
	// and centre.
	a.listEnemyWalls(o)
	var nearV, sx, sz, n int64
	for _, list := range [2][]int32{s.members, s.stuck} {
		for _, i := range list {
			u := &o.Own[i]
			if atWall(a.enemyWalls, u.X, u.Z) {
				nearV += int64(u.Info.Value)
				sx += int64(u.X)
				sz += int64(u.Z)
				n++
			}
		}
	}
	dF := int32(isqrt(frontD))
	if n > 0 && nearV*4 >= s.present.value {
		if s.nearSince == 0 || dF+breachProgress < s.nearD {
			s.nearSince, s.nearD = b.Tick, dF // progress starts the clock again
		}
	} else {
		s.nearSince = 0
	}
	if s.nearSince != 0 && b.Tick-s.nearSince >= heldTicks(k.Persona.Skill) {
		if !s.walled {
			s.walled, s.wallSince, s.wallStr, s.breachLast = true, b.Tick, s.total.strength(), b.Tick
			a.stats.walled++
		}
		s.wallSeen = b.Tick
	} else if s.walled && !s.wallPiece && b.Tick-s.wallSeen > wallLinger {
		s.walled = false
	}
	if !s.walled {
		return false, false
	}
	// Bleeding at the wall, or no piece falling: pull back and cool the
	// target, as an engagement lost.
	if s.total.strength()*100 < s.wallStr*(100-breachLoss) || b.Tick-s.breachLast > breachGiveUp {
		s.why = whyWall
		if s.target >= 0 {
			z := &a.zones[s.target]
			z.coolUntil = b.Tick + coolTicks
			z.coolStrength = s.total.strength()
		}
		s.walled, s.wallPiece, s.nearSince = false, false, 0
		a.retreat(b, s)
		return false, true
	}
	if !s.wallPiece && n > 0 {
		s.wallX, s.wallZ, s.wallPiece = a.breachPiece(b, s, int32(sx/n), int32(sz/n))
		if s.wallPiece {
			k.WatchWalls(s.wallX, s.wallZ)
		}
	}
	if !s.wallPiece {
		return true, false
	}
	s.wallAt = b.Tick
	b.BreachX, b.BreachZ, b.BreachTick = s.wallX, s.wallZ, b.Tick
	a.breachFire(b, s)
	return true, false
}

// listEnemyWalls keeps the listed pieces that are not ours.
func (a *Army) listEnemyWalls(o *aikit.Obs) {
	a.enemyWalls = a.enemyWalls[:0]
	for wi := range o.Walls {
		if w := &o.Walls[wi]; !ownWall(o, w.X, w.Z) {
			a.enemyWalls = append(a.enemyWalls, *w)
		}
	}
}

// atWall reports whether (x, z) lies within heldNear of one of the pieces.
func atWall(walls []aikit.Wall, x, z int32) bool {
	for i := range walls {
		if aikit.Dist2(x, z, walls[i].X, walls[i].Z) <= heldNear*heldNear {
			return true
		}
	}
	return false
}

// breachPiece picks the piece to break: among the listed pieces that are
// not ours and not behind the held members (relative to the target), the
// one nearest them, its distance raised by the enemy's static fire reaching
// it in proportion to the squad's own present fire and halved beside the
// piece that fell last.
func (a *Army) breachPiece(b *core.Board, s *squad, hx, hz int32) (int32, int32, bool) {
	dx, dz := int64(s.tx-hx), int64(s.tz-hz)
	l := max(isqrt(dx*dx+dz*dz), 1)
	dps := max(s.present.dps, 1)
	best := -1
	var bestC int64
	for wi := range a.enemyWalls {
		w := &a.enemyWalls[wi]
		vx, vz := int64(w.X-hx), int64(w.Z-hz)
		if (vx*dx+vz*dz)/l < -96 {
			continue
		}
		c := (int64(aikit.Dist(w.X, w.Z, hx, hz)) + 32) * (1000 + 3000*min(int64(a.static.At(w.X, w.Z)), dps)/dps) / 1000
		if s.breachLast > s.wallSince && aikit.Dist2(w.X, w.Z, s.fellX, s.fellZ) <= 48*48 {
			c /= 2
		}
		if best < 0 || c < bestC {
			best, bestC = wi, c
		}
	}
	if best < 0 {
		return 0, 0, false
	}
	return a.enemyWalls[best].X, a.enemyWalls[best].Z, true
}

// breachFire orders the members whose weapons damage walls, within their
// range of the piece plus breachReach, to fire at it, and the members near
// it that cannot damage it and stand in enemy fire back behind it, away
// from the target.
func (a *Army) breachFire(b *core.Board, s *squad) {
	o := b.O
	a.tmp = a.tmp[:0]
	a.tmp2 = a.tmp2[:0]
	for _, list := range [2][]int32{s.members, s.stuck} {
		for _, i := range list {
			u := &o.Own[i]
			info := u.Info
			if a.unit(u).withdraw != 0 || info.Role.Has(aikit.RoleAir) {
				continue
			}
			d := aikit.Dist2(u.X, u.Z, s.wallX, s.wallZ)
			if a.cls(info).wallDPS > 0 {
				if r := int64(info.Range + breachReach); d <= r*r {
					a.tmp = append(a.tmp, i)
				}
			} else if d <= (holdBack+heldNear)*(holdBack+heldNear) && a.danger.At(u.X, u.Z) > 0 {
				a.tmp2 = append(a.tmp2, i)
			}
		}
	}
	a.collect(b, a.tmp, okBreach, s.wallX, s.wallZ, 0, 0)
	if len(a.buf) > 0 && a.spend(true) {
		b.K.AttackPos(a.buf, s.wallX, s.wallZ, false)
		a.record(b, okBreach, s.wallX, s.wallZ, 0, 0)
		a.stats.breachOrders++
	}
	if len(a.tmp2) == 0 {
		return
	}
	dx, dz := int64(s.wallX-s.tx), int64(s.wallZ-s.tz)
	l := max(isqrt(dx*dx+dz*dz), 1)
	m := b.K.Map
	hx := min(max(s.wallX+int32(dx*holdBack/l), 0), m.WorldW)
	hz := min(max(s.wallZ+int32(dz*holdBack/l), 0), m.WorldH)
	a.collect(b, a.tmp2, okHold, hx, hz, 0, 0)
	if len(a.buf) > 0 && a.spend(true) {
		b.K.Move(a.buf, hx, hz, false)
		a.record(b, okHold, hx, hz, 0, 0)
	}
}

// listedWall reports whether the observation lists a piece centred at
// (x, z).
func listedWall(o *aikit.Obs, x, z int32) bool {
	for i := range o.Walls {
		if o.Walls[i].X == x && o.Walls[i].Z == z {
			return true
		}
	}
	return false
}

// ownWall reports whether a piece stands by an own or allied building:
// a wall that guards our side, which the army never breaks.
func ownWall(o *aikit.Obs, x, z int32) bool {
	for i := range o.Own {
		u := &o.Own[i]
		if !u.Info.Role.Has(aikit.RoleMobile) && aikit.Dist2(u.X, u.Z, x, z) <= breachOwn*breachOwn {
			return true
		}
	}
	for i := range o.Allies {
		u := &o.Allies[i]
		if u.Info != nil && !u.Info.Role.Has(aikit.RoleMobile) && aikit.Dist2(u.X, u.Z, x, z) <= breachOwn*breachOwn {
			return true
		}
	}
	return false
}

// isqrt is the integer square root of v (0 for v ≤ 0).
func isqrt(v int64) int64 {
	if v <= 0 {
		return 0
	}
	r := int64(1)
	for r*r <= v {
		r <<= 1
	}
	lo, hi := r>>1, r
	for lo < hi-1 {
		m := (lo + hi) / 2
		if m*m <= v {
			lo = m
		} else {
			hi = m
		}
	}
	return lo
}

package orders

// Nanolathe Modern policy: DESIGN_UNITS_ORDERS_COB "Modern patrol work" and
// "Smarter patrol — 2026-10-22". Selection borrows the ordinary work rows,
// never their resource arithmetic, worker state or simulation random stream,
// and draws no randomness of its own [I4]. Every distance below is in whole
// world units held in int64, with an exact integer square root [I2].

import (
	"math/bits"

	"github.com/nanolathe-gg/nanolathe/internal/combat"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"github.com/nanolathe-gg/nanolathe/internal/world"
)

// modernPatrolReach is twice the builder's sight: the route leash a
// candidate must satisfy and the stray bound a running job must keep.
func modernPatrolReach(sight int32) int32 { return 2 * sight }

// The caller has installed the original patrol leg and its maintenance gate.
// A hold reloads the new work head without code 3's selection-time RNG draw.
// The executor still makes its ordinary approach and work draws [I4].
func (r *ModernRules) PatrolWorkVisit(u *units.Unit, n *Node, tick uint32) (Code, bool) {
	if !modernPatrolBuilder(u, n) {
		return 0, false
	}
	if q := QueueOfUnit(u); q != nil {
		q.patrolWork.scanDue = tick + modernPatrolScanTicks
	}
	r.selectPatrolWork(u, n, tick)
	return 2, true
}

// PatrolWorkWake runs the same selection every modernPatrolScanTicks while
// the leg is installed and its head is blocked, between the 60-tick ground
// and 45-tick air maintenance visits. It never reinstalls the leg, arms its
// deadline or dispatches its handler, so the maintenance cadence and its
// movement payload are unchanged until a job is actually borrowed.
func (r *ModernRules) PatrolWorkWake(u *units.Unit, n *Node, tick uint32) bool {
	if n == nil || n.Phase != 1 || n.DynamicGate&gateMoveOutcomes == 0 || !modernPatrolBuilder(u, n) {
		return false
	}
	q := QueueOfUnit(u)
	if q == nil || q.Head() != n || int32(tick-q.patrolWork.scanDue) < 0 {
		return false
	}
	q.patrolWork.scanDue = tick + modernPatrolScanTicks
	// The air row's low-health pad seek precedes work selection and owns its
	// bounded pick [04 R-ORD-01 §7]. A damaged aircraft with a pad in reach
	// leaves selection to that maintenance visit; the filter draws nothing.
	if u.Def.CanFly && r.PatrolWork(PatrolWorkRequest{Builder: u}) != PatrolReclaimOnly && combat.AirBelowThreeQuarters(u) && len(airBasePads(u)) > 0 {
		return false
	}
	return r.selectPatrolWork(u, n, tick)
}

func modernPatrolBuilder(u *units.Unit, n *Node) bool {
	return u != nil && u.Def != nil && n != nil && u.Def.Builder && hasMover(u) && modernWorkPatrol(n)
}

// selectPatrolWork scores every admitted candidate and borrows the best one.
// It reports whether it head-inserted a job.
//
// A unit candidate is a friendly unit within or on the builder's sight circle
// and within or on twice that radius of the current leg, not remembered
// (directly, or through the factory carrying it), whose command code 8
// resolves. Construction needs nothing more; repair also needs energy at
// least one fifth of storage, and a moving patient must already be within
// build range. A feature candidate is an authored automatic-reclaim feature
// yielding a resource below capacity, under the same circle and leash, not
// animating, not remembered and not targeted by another friendly unit's
// running reclaim or resurrection.
func (r *ModernRules) selectPatrolWork(u *units.Unit, n *Node, tick uint32) bool {
	b, q := bindingFor(u), QueueOfUnit(u)
	if b == nil || q == nil {
		return false
	}
	option := r.PatrolWork(PatrolWorkRequest{Builder: u})
	resources, hasResources := playerResources(u)
	unitWork := option != PatrolReclaimOnly && r.AllowAutomaticRepair(u, tick)
	featureWork := option != PatrolAssistOnly && hasResources && u.Def.CanReclamate
	if !unitWork && !featureWork {
		return false
	}
	repairEnergy := hasResources && patrolResourceAtLeastTwenty(resources.Stock[1], resources.Capacity[1])
	s := newModernPatrolScore(q, u, n)
	var candidates []*units.Unit
	var claims modernPatrolClaims
	b.ForEachUnit(func(h pool.Handle, other *units.Unit) bool {
		if other == nil || !other.Alive {
			return scanNext
		}
		claims.observe(b, u, other, unitWork, featureWork)
		if unitWork && s.admitsUnit(b, h, other, repairEnergy) {
			candidates = append(candidates, other)
		}
		return scanNext
	})
	// A helper only raises a score and leaves its tie-breaks alone, and a
	// candidate whose command code 8 does not resolve is never picked, so
	// both are settled only for a candidate that could still win. The pick is
	// the same as scoring every candidate in full.
	var best modernPatrolPick
	for _, candidate := range candidates {
		pick := s.unitPick(candidate, 0)
		if !pick.before(best) {
			continue
		}
		if helpers := claims.helpers(candidate.Handle); helpers != 0 {
			if pick.score += helpers * s.quarter; !pick.before(best) {
				continue
			}
		}
		if pick.id = Resolve(8, u, candidate, nil); pick.id != 0 {
			best = pick
		}
	}
	if featureWork {
		feature := modernPatrolFeature(b, &s, resources, claims.features, true)
		if b.World != nil && b.World.verifyFeatureScan {
			if full := modernPatrolFeature(b, &s, resources, claims.features, false); full != feature {
				panic("orders: the restricted patrol feature scan chose differently from the full scan")
			}
		}
		if feature.before(best) {
			best = feature
		}
	}
	if !best.found {
		return false
	}
	if best.unit != nil {
		work := NewNodeForOrder(best.id, best.unit.Handle, best.unit.X, best.unit.Y, best.unit.Z, tick, u.Handle, false)
		work.workFactory = modernPatrolFactoryOf(u, best.unit)
		modernBorrowWork(u, n, work)
		return true
	}
	id := rowReclaim
	if u.Def.CanFly {
		id = rowVTOLReclaim
	}
	modernBorrowWork(u, n, NewNodeForOrder(id, 0, best.feature.X, best.feature.Y, best.feature.Z, tick, u.Handle, false))
	return true
}

// modernPatrolClaims is derived from other friendly units' current queue
// heads during the selection's one unit-pool walk; nothing stores a claim.
type modernPatrolClaims struct {
	units    []pool.Handle
	features [][2]int32
}

// observe reads one unit's running record. Repair and construction claims
// are counted, never exclusive: a unit's own producer (a constructor's
// MobileBuild, a factory's BuildingBuild) counts as one more builder on it.
// A running reclaim or resurrection claims its feature's anchor cell.
func (c *modernPatrolClaims) observe(b *QueueBinding, u, other *units.Unit, unitClaims, featureClaims bool) {
	if other == u {
		return
	}
	oq := QueueOfUnit(other)
	if oq == nil || len(oq.primary) == 0 || oq.primary[0] == nil {
		return
	}
	head := oq.primary[0]
	switch head.ID {
	case rowHelpBuild, rowVTOLHelpBuild, rowRepairUnit, rowVTOLRepairUnit, rowMobileBuild, rowVTOLMobileBuild, rowBuildingBuild:
		if unitClaims && head.Target != 0 && !scanHostile(b, u, other) {
			c.units = append(c.units, head.Target)
		}
	case rowReclaim, rowVTOLReclaim, rowResurrect:
		if featureClaims && !scanHostile(b, u, other) {
			cx, cz := world.WorldToCell(head.GoalX), world.WorldToCell(head.GoalZ)
			if feature, ok := b.LookupFeature(cx, cz); ok {
				cx, cz = feature.CX, feature.CZ
			}
			c.features = append(c.features, [2]int32{cx, cz})
		}
	}
}

func (c *modernPatrolClaims) helpers(h pool.Handle) int64 {
	var count int64
	for _, claimed := range c.units {
		if claimed == h {
			count++
		}
	}
	return count
}

func modernPatrolClaimed(claims [][2]int32, feature FeatureView) bool {
	for _, c := range claims {
		if c[0] == feature.CX && c[1] == feature.CZ {
			return true
		}
	}
	return false
}

// modernPatrolScore holds one selection's geometry. The leg runs from the
// previous waypoint to the assignment's goal. The previous waypoint is the
// last other record of the same patrol row in the front segment: rotation
// sends an arrived waypoint to the tail, and the chain setup appended the
// patrol's starting position there before the first leg ran
// [04 R-ORD-01 §4]. A queue with no such record has no leash.
type modernPatrolScore struct {
	q              *Queue
	u              *units.Unit
	bx, bz, ex, ez int64
	sx, sz         int64
	leg            bool
	toEnd          int64 // |builder → leg end|
	quarter        int64 // a quarter of sight: the reclaim offset and one helper's penalty
	reach          int64 // the route leash
}

func newModernPatrolScore(q *Queue, u *units.Unit, n *Node) modernPatrolScore {
	s := modernPatrolScore{q: q, u: u, bx: modernWhole(u.X), bz: modernWhole(u.Z), ex: modernWhole(n.GoalX), ez: modernWhole(n.GoalZ)}
	s.quarter = int64(u.Def.SightDistance / 4)
	s.reach = int64(modernPatrolReach(u.Def.SightDistance))
	for i := len(q.primary) - 1; i >= 0; i-- {
		if rec := q.primary[i]; rec != nil && rec != n && rec.ID == n.ID && rec.Flags&FlagTombstone == 0 {
			s.sx, s.sz, s.leg = modernWhole(rec.GoalX), modernWhole(rec.GoalZ), true
			break
		}
	}
	s.toEnd = modernIsqrt(modernWholeDistanceSquared(s.bx, s.bz, s.ex, s.ez))
	return s
}

// leashed reports whether a point lies within or on the route leash of the
// closed leg segment.
func (s *modernPatrolScore) leashed(x, z numeric.Fixed) bool {
	return !s.leg || modernWithinSegment(modernWhole(x), modernWhole(z), s.sx, s.sz, s.ex, s.ez, s.reach)
}

func (s *modernPatrolScore) admitsUnit(b *QueueBinding, h pool.Handle, target *units.Unit, repairEnergy bool) bool {
	u := s.u
	if !modernPatrolCandidate(u, target.X, target.Z) || !repairCandidate(b, u, h, target) || !s.leashed(target.X, target.Z) {
		return false
	}
	if target.Remaining == 0 {
		if !repairEnergy {
			return false
		}
		// The scalar speed word is the mover's current speed [04 §8.1].
		if target.Move.Speed != 0 && !inBuildRangeOf(u, target) {
			return false
		}
	}
	memory := &s.q.patrolWork
	if memory.remembers(patrolMemoryUnit, target) {
		return false
	}
	if target.Attachment.Carrier != 0 && memory.remembersAnyFactory() && memory.remembers(patrolMemoryFactory, lookupTarget(u, target.Attachment.Carrier)) {
		return false
	}
	return true
}

// modernPatrolPick is one scored candidate. Lower scores win; ties go to the
// nearer candidate, then to unit work before reclaim, then to the unit handle
// or the feature identity order. That is a strict total order, so the pick is
// the same in any visiting order.
type modernPatrolPick struct {
	found    bool
	score    int64
	distance int64 // squared, builder → candidate
	unit     *units.Unit
	id       ID
	feature  FeatureView
}

func (p modernPatrolPick) before(q modernPatrolPick) bool {
	switch {
	case !p.found:
		return false
	case !q.found:
		return true
	case p.score != q.score:
		return p.score < q.score
	case p.distance != q.distance:
		return p.distance < q.distance
	case (p.unit != nil) != (q.unit != nil):
		return p.unit != nil
	case p.unit != nil:
		return p.unit.Handle < q.unit.Handle
	}
	return modernFeatureBefore(p.feature, q.feature)
}

// detour is |builder→target| + |target→leg end| − |builder→leg end|.
func (s *modernPatrolScore) detour(x, z numeric.Fixed) (detour, distance int64) {
	tx, tz := modernWhole(x), modernWhole(z)
	distance = modernWholeDistanceSquared(s.bx, s.bz, tx, tz)
	return modernIsqrt(distance) + modernIsqrt(modernWholeDistanceSquared(tx, tz, s.ex, s.ez)) - s.toEnd, distance
}

func (s *modernPatrolScore) unitPick(target *units.Unit, helpers int64) modernPatrolPick {
	detour, distance := s.detour(target.X, target.Z)
	return modernPatrolPick{found: true, score: detour + helpers*s.quarter, distance: distance, unit: target}
}

func (s *modernPatrolScore) featurePick(f FeatureView) modernPatrolPick {
	detour, distance := s.detour(f.X, f.Z)
	return modernPatrolPick{found: true, score: detour + s.quarter, distance: distance, feature: f}
}

// modernPatrolFeature is the patrol's best reclaim candidate. near restricts
// the traversal to the square around the sight radius, which holds every
// feature modernPatrolCandidate admits, so the restricted and full scans see
// the same candidates and, under the strict total order, make the same pick.
func modernPatrolFeature(b *QueueBinding, s *modernPatrolScore, resources ResourceView, claims [][2]int32, near bool) modernPatrolPick {
	var best modernPatrolPick
	u := s.u
	visit := func(feature FeatureView) bool {
		if !feature.Reclaimable || !feature.Autoreclaimable || feature.Animating || !modernPatrolCandidate(u, feature.X, feature.Z) ||
			!modernFeatureNeeded(feature, resources) || !s.leashed(feature.X, feature.Z) ||
			s.q.patrolWork.remembersFeature(feature.CX, feature.CZ) || modernPatrolClaimed(claims, feature) {
			return scanNext
		}
		if pick := s.featurePick(feature); pick.before(best) {
			best = pick
		}
		return scanNext
	}
	if near {
		b.ForEachFeatureNear(u.X, u.Z, u.Def.SightDistance, visit)
	} else {
		b.ForEachFeature(visit)
	}
	return best
}

func modernFeatureNeeded(feature FeatureView, resources ResourceView) bool {
	return feature.Metal > 0 && resources.Stock[0] < resources.Capacity[0] ||
		feature.Energy > 0 && resources.Stock[1] < resources.Capacity[1]
}

func modernFeatureBefore(a, b FeatureView) bool {
	if a.CZ != b.CZ {
		return a.CZ < b.CZ
	}
	if a.CX != b.CX {
		return a.CX < b.CX
	}
	return a.ID < b.ID
}

// modernWhole is the whole-unit part of a 16.16 coordinate word, floored.
func modernWhole(v numeric.Fixed) int64 { return int64(int32(v.Raw()) >> 16) }

func modernWholeDistanceSquared(ax, az, bx, bz int64) int64 {
	dx, dz := ax-bx, az-bz
	return dx*dx + dz*dz
}

// modernIsqrt is the floor of the square root of n, zero for n <= 0.
func modernIsqrt(n int64) int64 {
	if n <= 0 {
		return 0
	}
	x := int64(1) << ((bits.Len64(uint64(n)) + 1) / 2) // at least sqrt(n)
	for {
		y := (x + n/x) / 2
		if y >= x {
			return x
		}
		x = y
	}
}

// modernWithinSegment reports whether point p lies within or on radius of
// the closed segment a–b, in whole units. The perpendicular test compares
// cross² with radius²·length² in double-word arithmetic, so no division or
// rounding enters the boundary.
func modernWithinSegment(px, pz, ax, az, bx, bz, radius int64) bool {
	if radius < 0 {
		return false
	}
	r2 := radius * radius
	dx, dz := bx-ax, bz-az
	wx, wz := px-ax, pz-az
	length := dx*dx + dz*dz
	dot := wx*dx + wz*dz
	if length == 0 || dot <= 0 {
		return wx*wx+wz*wz <= r2
	}
	if dot >= length {
		return modernWholeDistanceSquared(px, pz, bx, bz) <= r2
	}
	cross := wx*dz - wz*dx
	return !modernProduct(r2, length).less(modernProduct(cross, cross))
}

package utility

import (
	"github.com/nanolathe-gg/nanolathe/internal/aikit"
	"github.com/nanolathe-gg/nanolathe/internal/aikit/core"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
)

// The gate network (docs/DESIGN_SESSIONS_AI_SAVE.md "Modern AI gates"). A
// gate (aikit RoleGate: Talon's Astria Porta) is dialed by an ordinary ground
// attack: its aim script picks the gate nearest the aimed point, within 300
// world units of it, and never fires; a unit that comes within 30 world
// units of a linked gate's pad is attached to it and handed to the peer,
// which drops it on its own pad; the peer needs no link of its own, and the
// off command unlinks (research/extensions/talon-engine.md "Gate network").
// The move a unit was given behind the pad does not survive the hand-off:
// it is released idle on the receiving pad, and a unit standing there blocks
// the next one's release.
//
// The brain keeps one gate in the base and one toward the front. The front
// is where the army sends its units, read from their own orders (the army
// layer sends new units to its gathering point): a trip is a ground combat
// unit of ours walking to a goal for which the pair can be planned and whose
// walk the pair would shorten.
//
//   - Building. Once three trips were seen in the last two minutes, it is
//     past minute five, the income pays for both gates in ninety seconds of
//     metal and of energy, and a builder other than the commander can make a
//     gate (the front one lies beyond the commander's leash), an idle
//     builder scores the base gate beside home, toward the latest trip's goal
//     and away from the side field, and once that one stands the front gate,
//     at a higher weight, 300 world units short of that goal, so the units
//     gathering there leave its pad clear. The front site is no more than
//     halfway to the enemy base and no more in the enemy's reach than the
//     site-threat half point. A front gate that was lost is not replaced for
//     three minutes.
//   - Dialing. While both stand, the base gate is dialed to the front gate.
//     A repeated aim heading is discarded by the script, so each new dial
//     aims at the next of four points within 96 world units of the gate.
//   - Sending. After the dialing animation, ground combat units moving to a
//     goal are sent to the base gate's pad when the walk to the pad plus the
//     walk from the front gate to their goal is at most seventy percent of
//     their walk, at most three on their way or waiting at once, since the
//     pad carries one at a time; one that stands idle beside the pad, which was taken when
//     it arrived, is sent onto it again; and units idle on the front gate's
//     pad are moved on to the goal they had, or on toward the latest trip's
//     goal. The army (tactics) re-sends a unit only once it is idle away from
//     its order's point, so it carries a delivered unit on itself.
//   - Failure. If a unit sent through stands free at the base pad for
//     fifteen seconds before any unit was ever carried, the gates do not
//     carry units in this battle: they are no longer used or built.
//
// These distances and times are planning choices, not game rules.
const (
	gateWeight      = 1000 // the base gate: nominal 100, scaled as the economy's weights are (×10)
	gateFrontWeight = 2500 // the front gate, which makes the standing base gate of use
	gateMinSpan     = 1600 // the two gates at least this far apart
	gateBack        = 300  // the front gate this far short of the army's goal
	gateBaseR       = 1200 // a gate this close to home is the base gate
	gateBaseFront   = 250  // the base gate's site: this far toward the front of home
	gateBaseSide    = 450  // and this far to the side away from the side field
	gateIncomeSecs  = 90   // both gates' cost within this many seconds of income
	gateFrom        = 5    // minutes before which no gate is built
	gateLinkTicks   = 600  // ticks after a dial before units are sent (the dialing animation)
	gateSave        = 70   // percent: sent when the gate trip is at most this share of the walk
	gateDetour      = 200  // world units the hand-off is counted as
	gatePadR        = 64   // an idle unit this close to the receiving pad is moved on
	gateOnward      = 200  // how far on from the receiving gate a unit with no goal is moved
	gateTrips       = 16   // recent trips remembered
	gateEntryR      = 48   // a sent unit this close to the sending pad ...
	gateCaptureWait = 450  // ... and free this long, with nothing ever carried: the gates fail
	gateResend      = 1800 // ticks before a unit is sent through again
	gateRetryR      = 200  // a sent unit idle this close to the sending pad ...
	gateRetryTicks  = 90   // ... this long after it was sent is sent onto the pad again
	gateInFlight    = 3    // at most this many units sent and not yet carried ...
	gateFlightTicks = 1800 // ... in this many ticks: the pad takes one at a time
	gateDemandTicks = 3600 // trips seen this recently make the gates wanted
	gateDemandUnits = 3    // this many of them
	gateRebuildRest = 5400 // ticks after a front gate is lost before another is placed
	gateGroups      = 2    // move-on commands (distinct goals) per think
)

// gateOffsets are the aim points about the receiving gate, one per dial.
var gateOffsets = [...][2]int32{{0, 0}, {96, 0}, {0, 96}, {-96, 0}}

// gateUnit is one of our gates as this think sees it.
type gateUnit struct {
	own   int32 // index in O.Own, -1 for none
	h     pool.Handle
	gen   uint32
	x, z  int32
	built bool
}

var noGate = gateUnit{own: -1}

// gateDial is the base gate's last dial.
type gateDial struct {
	h, to      pool.Handle
	gen, toGen uint32
	tick       uint32
	n          uint32 // dials so far: picks the aim offset
}

// gateRoute is a unit sent through the gates; def and gen say which unit.
type gateRoute struct {
	def    *aikit.UnitInfo
	gen    uint32
	tick   uint32 // when it was sent, 0 never
	gx, gz int32  // the goal it was walking to
	near   uint32 // first think it stood free at the sending pad, 0 none
	lx, lz int32  // where it stood at the last think
	done   bool   // carried: it jumped to the receiving gate
	seen   bool   // its trip to (sx, sz) was counted (gateFront)
	sx, sz int32
}

// gateTrip is a trip of one ground unit the pair would have shortened: when
// it was seen, its goal, and the sites planned for that goal.
type gateTrip struct {
	tick           uint32
	gx, gz, bx, bz int32
	fx, fz         int32
}

// gateGroup is units sent toward one goal this think.
type gateGroup struct {
	gx, gz int32
	units  []pool.Handle
}

type gateState struct {
	init    bool
	defs    int32 // gate definitions our build tree reaches; 0 skips everything
	tick    uint32
	fresh   bool
	base    gateUnit
	fwd     gateUnit
	sites   bool // the planned sites below exist this think
	bx, bz  int32
	fx, fz  int32
	gx, gz  int32  // the goal of the latest trip the pair would shorten
	demand  uint32 // last think gateDemandUnits such trips were recent
	dial    gateDial
	failed  bool
	trips   [gateTrips]gateTrip
	next    int
	lastFwd gateUnit
	lostFwd uint32
	routes  []gateRoute
	groups  [gateGroups]gateGroup // move-on commands this think, one per goal
	send    []pool.Handle         // the units sent through this think
	// Counters for the arena (Economy.Report).
	peakBuilt, dials, sent, delivered int32
	why                               [gateWhys]int32  // evaluations refused, by reason
	front                             [frontWhys]int32 // trips looked at, by what they gave
}

// What a trip looked at gave, counted for the arena.
const (
	frontNear    = iota // the goal is too near home
	frontThreat         // the front site is in the enemy's reach
	frontFar            // the front site is past halfway to the enemy base
	frontSpan           // the front site is too near the base gate
	frontWalk           // the pair would not shorten this unit's walk
	frontPlanned        // counted
	frontWhys
)

var frontWhyNames = [frontWhys]string{"near", "threat", "far", "span", "walk", "counted"}

// Why evalGate offered no gate, counted for the arena.
const (
	whyFailed  = iota // the gates were found not to carry
	whySites          // no recent trip the pair would shorten
	whyEarly          // before minute gateFrom
	whyDemand         // fewer than gateDemandUnits such trips lately
	whyIncome         // the income cannot pay for both, or energy stalls
	whyBuilder        // no builder but the commander can make one
	whyHave           // both stand or are on their way, or the front one rests
	whyLeash          // the commander, and the site beyond its leash
	whyOffered        // offered
	gateWhys
)

var gateWhyNames = [gateWhys]string{"failed", "sites", "early", "demand", "income", "builder", "have", "leash", "offered"}

func isGround(info *aikit.UnitInfo) bool {
	d := info.Def
	return d != nil && !d.CanFly && !d.CanHover && !info.Role.Any(aikit.RoleAir|aikit.RoleNaval|aikit.RoleHover)
}

// dialed reports whether the base gate was dialed to the front gate as they
// stand now.
func (st *gateState) dialed() bool {
	d := &st.dial
	return st.base.own >= 0 && st.fwd.own >= 0 && d.h == st.base.h && d.gen == st.base.gen && d.to == st.fwd.h && d.toGen == st.fwd.gen
}

// linked reports a dial old enough for the link to stand.
func (st *gateState) linked(tick uint32) bool {
	return st.dialed() && tick-st.dial.tick >= gateLinkTicks
}

func (st *gateState) routeOf(s *shared, u *aikit.OwnUnit) *gateRoute {
	st.routes = handleSlots(st.routes, u.H, s.hcap)
	r := &st.routes[u.H]
	if r.def != u.Info || r.gen != u.Gen {
		*r = gateRoute{def: u.Info, gen: u.Gen}
	}
	return r
}

// gates returns this think's picture of the gate network, building it on
// first use. A side whose build tree reaches no gate (every stock side)
// costs one test.
func (e *Economy) gates(b *core.Board) *gateState {
	s := e.s
	st := &e.ab.gates
	if !st.init {
		st.init = true
		for _, u := range s.k.Table.Units {
			if u.Role.Has(aikit.RoleGate) && s.k.Owns(u) {
				st.defs++
			}
		}
	}
	if st.defs == 0 || (st.fresh && st.tick == s.tick) {
		return st
	}
	st.fresh, st.tick = true, s.tick
	o := b.O
	st.base, st.fwd = noGate, noGate
	var baseD int64
	for i := range o.Own {
		u := &o.Own[i]
		if !u.Info.Role.Has(aikit.RoleGate) {
			continue
		}
		if d := aikit.Dist2(u.X, u.Z, b.HomeX, b.HomeZ); d <= gateBaseR*gateBaseR && (st.base.own < 0 || d < baseD) {
			st.base, baseD = gateUnit{own: int32(i), h: u.H, gen: u.Gen, x: u.X, z: u.Z, built: u.Built}, d
		}
	}
	var fwdD int64
	for i := range o.Own {
		u := &o.Own[i]
		if !u.Info.Role.Has(aikit.RoleGate) || int32(i) == st.base.own {
			continue
		}
		if d := aikit.Dist2(u.X, u.Z, b.HomeX, b.HomeZ); d > gateBaseR*gateBaseR && d > fwdD {
			st.fwd, fwdD = gateUnit{own: int32(i), h: u.H, gen: u.Gen, x: u.X, z: u.Z, built: u.Built}, d
		}
	}
	if l := &st.lastFwd; l.own >= 0 && l.built && (st.fwd.h != l.h || st.fwd.gen != l.gen) {
		st.lostFwd = s.tick
	}
	st.lastFwd = st.fwd
	n := int32(0)
	for _, g := range [...]*gateUnit{&st.base, &st.fwd} {
		if g.own >= 0 && g.built {
			n++
		}
	}
	st.peakBuilt = max(st.peakBuilt, n)
	e.gateFront(b, st)
	return st
}

// gateFront counts the trips of ground units of ours the pair would shorten:
// a unit walking to a goal for which gateSites plans the pair, and whose walk
// the pair (the gates standing, or the planned sites) would shorten, counts
// once per goal. The latest recent trip gives the sites, and gateDemandUnits
// trips in the last gateDemandTicks make the gates wanted. The front is thus
// where the army sends its units, as their own orders show: the army layer
// sends new units to its gathering point.
func (e *Economy) gateFront(b *core.Board, st *gateState) {
	s := e.s
	for _, i := range b.Combat {
		u := &b.O.Own[i]
		if !traveller(u) {
			continue
		}
		r := st.routeOf(s, u)
		if r.seen && aikit.Dist2(r.sx, r.sz, u.GoalX, u.GoalZ) <= gateBack*gateBack {
			continue
		}
		bx, bz, fx, fz, why := e.gateSites(b, st, u.GoalX, u.GoalZ)
		if why == frontPlanned {
			sbx, sbz, sfx, sfz := bx, bz, fx, fz
			if st.base.own >= 0 {
				sbx, sbz = st.base.x, st.base.z
			}
			if st.fwd.own >= 0 {
				sfx, sfz = st.fwd.x, st.fwd.z
			}
			if !gateSaves(u.X, u.Z, sbx, sbz, sfx, sfz, u.GoalX, u.GoalZ) {
				why = frontWalk
			}
		}
		st.front[why]++
		r.seen, r.sx, r.sz = true, u.GoalX, u.GoalZ
		if why != frontPlanned {
			continue
		}
		st.trips[st.next] = gateTrip{tick: s.tick, gx: u.GoalX, gz: u.GoalZ, bx: bx, bz: bz, fx: fx, fz: fz}
		st.next = (st.next + 1) % gateTrips
	}
	var n int32
	latest := -1
	for j := range st.trips {
		t := &st.trips[j]
		if t.tick == 0 || s.tick-t.tick > gateDemandTicks {
			continue
		}
		n++
		if latest < 0 || t.tick > st.trips[latest].tick {
			latest = j
		}
	}
	st.sites = latest >= 0
	if st.sites {
		t := &st.trips[latest]
		st.bx, st.bz, st.fx, st.fz, st.gx, st.gz = t.bx, t.bz, t.fx, t.fz, t.gx, t.gz
	}
	if n >= gateDemandUnits {
		st.demand = s.tick
	}
}

// gateSites plans where the two gates go for the army's goal (gx, gz): the
// front gate gateBack short of it on the way from home, the base gate
// beside home toward it and away from the side field. It reports why there
// are none when the front site would be in the enemy's reach above the
// site-threat half point, more than halfway to the enemy base, or nearer
// than gateMinSpan to the base gate (the standing one, or its site).
func (e *Economy) gateSites(b *core.Board, st *gateState, gx, gz int32) (bx, bz, fx, fz int32, why int) {
	s := e.s
	m := b.K.Map
	hx, hz := b.HomeX, b.HomeZ
	d := int64(aikit.Dist(hx, hz, gx, gz))
	if m == nil || d <= gateBack {
		return 0, 0, 0, 0, frontNear
	}
	// Unit vectors (permille) toward the goal and across, the cross one
	// pointing away from the side field where makers and storage go.
	ux, uz := int64(gx-hx)*1000/d, int64(gz-hz)*1000/d
	fx = clampWorld(gx-int32(ux*gateBack/1000), m.WorldW)
	fz = clampWorld(gz-int32(uz*gateBack/1000), m.WorldH)
	if b.Threat != nil && b.Threat.At(fx, fz) > s.p.ThreatHalf {
		return 0, 0, 0, 0, frontThreat
	}
	if 4*aikit.Dist2(hx, hz, fx, fz) > aikit.Dist2(hx, hz, b.EnemyX, b.EnemyZ) {
		return 0, 0, 0, 0, frontFar
	}
	px, pz := -uz, ux
	if int64(s.sideX-hx)*px+int64(s.sideZ-hz)*pz > 0 {
		px, pz = -px, -pz
	}
	bx = clampWorld(hx+int32((ux*gateBaseFront+px*gateBaseSide)/1000), m.WorldW)
	bz = clampWorld(hz+int32((uz*gateBaseFront+pz*gateBaseSide)/1000), m.WorldH)
	sx, sz := bx, bz
	if st.base.own >= 0 {
		sx, sz = st.base.x, st.base.z
	}
	if aikit.Dist2(sx, sz, fx, fz) < gateMinSpan*gateMinSpan {
		return 0, 0, 0, 0, frontSpan
	}
	return bx, bz, fx, fz, frontPlanned
}

// gateBuilder reports a built builder of ours other than the commander that
// can make gate p: the front gate lies beyond the commander's leash, so the
// pair is begun only once one exists (Talon's advanced constructors).
func gateBuilder(b *core.Board, p *aikit.UnitInfo) bool {
	for _, i := range b.Builders {
		u := &b.O.Own[i]
		if !u.Info.Role.Has(aikit.RoleCommander) && u.Info.Buildable(p) {
			return true
		}
	}
	return false
}

// gatePlans reports whether a builder of ours is committed to placing a base
// or a front gate: this think, or on its way (a commitment the economy
// keeps live as the shared model does).
func (e *Economy) gatePlans(b *core.Board) (base, fwd bool) {
	s := e.s
	for _, i := range b.Builders {
		u := &b.O.Own[i]
		c := s.commitIf(u)
		if c == nil || c.kind != cGate || (c.tick != s.tick && (u.Order == aikit.OrderIdle || s.tick-c.tick > 2700)) {
			continue
		}
		if aikit.Dist2(c.x, c.z, b.HomeX, b.HomeZ) <= gateBaseR*gateBaseR {
			base = true
		} else {
			fwd = true
		}
	}
	return base, fwd
}

// evalGate scores placing gate p: the base gate while there is none, then
// the front gate, when the army's travel, the time and the income ask for
// them; the nominal weight times the affordability, travel and site-threat
// considerations every building takes, and nothing during an energy stall.
func (e *Economy) evalGate(b *core.Board, u *aikit.OwnUnit, p *aikit.UnitInfo) cand {
	s := e.s
	c := cand{kind: cGate, prod: p, spot: -1, spacing: 2}
	if s.p.Gates == 0 || !s.k.Owns(p) {
		return c
	}
	st := e.gates(b)
	if st.defs == 0 {
		return c
	}
	why := whyOffered
	switch {
	case st.failed:
		why = whyFailed
	case !st.sites:
		why = whySites
	case s.minutes < gateFrom:
		why = whyEarly
	case st.demand == 0 || s.tick-st.demand > gateDemandTicks:
		why = whyDemand
	case s.mInc*gateIncomeSecs < 2*int64(p.Metal)*1000 || s.eInc*gateIncomeSecs < 2*int64(p.Energy)*1000 || (s.eStock*20 < s.eCap && s.eInc < s.eExp):
		why = whyIncome
	case !gateBuilder(b, p):
		why = whyBuilder
	}
	if why != whyOffered {
		st.why[why]++
		return c
	}
	basePlan, fwdPlan := e.gatePlans(b)
	weight := int64(gateWeight)
	switch {
	case st.base.own < 0 && !basePlan:
		c.x, c.z = st.bx, st.bz
	case st.base.own >= 0 && !basePlan && st.fwd.own < 0 && !fwdPlan && (st.lostFwd == 0 || s.tick-st.lostFwd >= gateRebuildRest):
		c.x, c.z = st.fx, st.fz
		weight = gateFrontWeight
	default:
		st.why[whyHave]++
		return c
	}
	if u.Info.Role.Has(aikit.RoleCommander) && s.comOff(b, c.x, c.z) {
		st.why[whyLeash]++
		return c
	}
	st.why[whyOffered]++
	dist := int64(aikit.Dist(u.X, u.Z, c.x, c.z))
	travel := half(dist/speedOf(u.Info), int64(s.p.TravelHalf))
	thr := int64(one)
	if b.Threat != nil {
		thr = half(int64(b.Threat.At(c.x, c.z)), int64(s.p.ThreatHalf))
	}
	aff := e.afford(p)
	c.score = mul(mul(mul(weight, aff), travel), thr)
	c.f = [4]int64{one, aff, travel, thr}
	return c
}

// serviceGates dials the base gate and sends units through, at most limit
// actions, and returns the actions used.
func (e *Economy) serviceGates(b *core.Board, limit int) int {
	s := e.s
	st := e.gates(b)
	if st.defs == 0 {
		return 0
	}
	if st.failed || limit <= 0 {
		return 0
	}
	used := 0
	if st.base.built && st.fwd.built && !st.dialed() {
		off := gateOffsets[st.dial.n%uint32(len(gateOffsets))]
		m := b.K.Map
		x, z := clampWorld(st.fwd.x+off[0], m.WorldW), clampWorld(st.fwd.z+off[1], m.WorldH)
		b.K.AttackPos([]pool.Handle{st.base.h}, x, z, false)
		st.dial = gateDial{h: st.base.h, gen: st.base.gen, to: st.fwd.h, toGen: st.fwd.gen, tick: s.tick, n: st.dial.n + 1}
		st.dials++
		used++
	}
	if !st.linked(s.tick) || !st.base.built || !st.fwd.built {
		return used
	}
	used += e.gateMoveOn(b, st, limit-used)
	used += e.gateSend(b, st, limit-used)
	e.gateWatch(b, st)
	return used
}

// gateSaves reports whether walking from (x, z) to the base point (bx, bz),
// and on from the front point (fx, fz) to the goal, is short enough against
// the walk to the goal for the gates to be worth it.
func gateSaves(x, z, bx, bz, fx, fz, gx, gz int32) bool {
	direct := int64(aikit.Dist(x, z, gx, gz))
	via := int64(aikit.Dist(x, z, bx, bz)) + int64(aikit.Dist(fx, fz, gx, gz)) + gateDetour
	return via*100 <= direct*gateSave
}

// traveller reports a ground combat unit of ours walking to a goal, free to
// be sent.
func traveller(u *aikit.OwnUnit) bool {
	return u.Built && u.Carrier == 0 && u.Order == aikit.OrderMove && (u.GoalX != 0 || u.GoalZ != 0) && isGround(u.Info)
}

// gateSend sends travellers to the base gate's pad in one group command, at
// most gateInFlight of them on their way or waiting at once, and remembers
// where each was going. A unit sent before that stands idle and uncarried
// near the pad (the pad was taken when it arrived) is sent onto it again.
func (e *Economy) gateSend(b *core.Board, st *gateState, limit int) int {
	s := e.s
	if limit <= 0 {
		return 0
	}
	// Units already on their way to the pad, or waiting at it: the pad
	// carries one at a time, and a unit kept waiting is taken back by the
	// army, so only a few are sent at once.
	flight := 0
	for _, i := range b.Combat {
		u := &b.O.Own[i]
		if int(u.H) < len(st.routes) {
			if r := &st.routes[u.H]; r.def == u.Info && r.gen == u.Gen && r.tick != 0 && !r.done && s.tick-r.tick < gateFlightTicks {
				flight++
			}
		}
	}
	units := st.send[:0]
	for _, i := range b.Combat {
		u := &b.O.Own[i]
		r := st.routeOf(s, u)
		switch {
		case traveller(u) && gateSaves(u.X, u.Z, st.base.x, st.base.z, st.fwd.x, st.fwd.z, u.GoalX, u.GoalZ):
			if (r.tick != 0 && s.tick-r.tick < gateResend) || flight >= gateInFlight {
				continue
			}
			flight++
			*r = gateRoute{def: u.Info, gen: u.Gen, tick: s.tick, gx: u.GoalX, gz: u.GoalZ, lx: u.X, lz: u.Z, seen: r.seen, sx: r.sx, sz: r.sz}
			st.sent++
		case r.tick != 0 && !r.done && u.Order == aikit.OrderIdle && u.Carrier == 0 &&
			aikit.Dist2(u.X, u.Z, st.base.x, st.base.z) <= gateRetryR*gateRetryR && s.tick-r.tick >= gateRetryTicks:
			r.tick = s.tick
		default:
			continue
		}
		units = append(units, u.H)
	}
	st.send = units
	if len(units) == 0 {
		return 0
	}
	b.K.Move(units, st.base.x, st.base.z, false)
	return 1
}

// gateMoveOn moves units of ours standing idle on the front gate's pad on:
// a unit sent through to the goal it had, any other mobile unit gateOnward
// toward the army's goal (or the enemy base). A builder is left to the
// economy.
func (e *Economy) gateMoveOn(b *core.Board, st *gateState, limit int) int {
	s := e.s
	if limit <= 0 {
		return 0
	}
	o := b.O
	ng := 0
	tx, tz := b.EnemyX, b.EnemyZ
	if st.demand != 0 && s.tick-st.demand <= gateDemandTicks {
		tx, tz = st.gx, st.gz
	}
	d := int64(aikit.Dist(st.fwd.x, st.fwd.z, tx, tz))
	m := b.K.Map
	ox, oz := st.fwd.x, st.fwd.z
	if d > 0 {
		ox = clampWorld(st.fwd.x+int32(int64(tx-st.fwd.x)*gateOnward/d), m.WorldW)
		oz = clampWorld(st.fwd.z+int32(int64(tz-st.fwd.z)*gateOnward/d), m.WorldH)
	}
	for i := range o.Own {
		u := &o.Own[i]
		if !u.Built || u.Carrier != 0 || u.Order != aikit.OrderIdle || !u.Info.Role.Has(aikit.RoleMobile) || u.Info.Role.Has(aikit.RoleBuilder) {
			continue
		}
		if aikit.Dist2(u.X, u.Z, st.fwd.x, st.fwd.z) > gatePadR*gatePadR {
			continue
		}
		gx, gz := ox, oz
		if r := st.routeOf(s, u); r.tick != 0 {
			gx, gz = r.gx, r.gz
		}
		g := -1
		for j := 0; j < ng; j++ {
			if st.groups[j].gx == gx && st.groups[j].gz == gz {
				g = j
				break
			}
		}
		if g < 0 {
			if ng >= gateGroups || ng >= limit {
				continue
			}
			g = ng
			st.groups[g].gx, st.groups[g].gz, st.groups[g].units = gx, gz, st.groups[g].units[:0]
			ng++
		}
		st.groups[g].units = append(st.groups[g].units, u.H)
	}
	for j := 0; j < ng; j++ {
		b.K.Move(st.groups[j].units, st.groups[j].gx, st.groups[j].gz, false)
	}
	return ng
}

// gateWatch counts a unit sent through as carried when it is seen near the
// front gate far from where it stood the think before (no walk covers that
// in one think), and marks the gates failed when a unit sent through has
// stood free at the base pad for gateCaptureWait ticks before any unit was
// carried.
func (e *Economy) gateWatch(b *core.Board, st *gateState) {
	s := e.s
	const jump = gateMinSpan / 2
	for _, i := range b.Combat {
		u := &b.O.Own[i]
		if int(u.H) >= len(st.routes) {
			continue
		}
		r := &st.routes[u.H]
		if r.def != u.Info || r.gen != u.Gen || r.tick == 0 || r.done {
			continue
		}
		jumped := aikit.Dist2(u.X, u.Z, r.lx, r.lz) > jump*jump && aikit.Dist2(u.X, u.Z, st.fwd.x, st.fwd.z) <= jump*jump
		r.lx, r.lz = u.X, u.Z
		if jumped {
			r.done = true
			st.delivered++
			continue
		}
		if st.delivered > 0 || u.Carrier != 0 || aikit.Dist2(u.X, u.Z, st.base.x, st.base.z) > gateEntryR*gateEntryR {
			r.near = 0
			continue
		}
		if r.near == 0 {
			r.near = s.tick
		} else if s.tick-r.near >= gateCaptureWait {
			st.failed = true
		}
	}
}

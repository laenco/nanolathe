package utility

import (
	"github.com/nanolathe-gg/nanolathe/internal/aikit"
	"github.com/nanolathe-gg/nanolathe/internal/aikit/core"
)

// Breaching walls. When the army reports a squad held at an enemy wall
// (Board.BreachX, tactics walls.go), one constructor goes to reclaim the
// pieces there (Kit.Breach): a builder reclaims a dragon's tooth in under a
// second, whatever its build power [05 R-WORK-01 §5], so one that reaches
// the wall opens it at once, where the army's guns take a minute or more.
// The constructor may well be lost, and is sent anyway: the cheapest one
// that can reclaim goes (the nearest among equals, the commander never),
// it does not turn home for the threat that would send another builder
// back (retreat), and it keeps its orders until they are done or the army
// has stopped asking for breachLapse (it pulled back), when it is called
// off. Only one is out at a time, at most one every breachEvery, and only
// while more than breachKeep other constructors remain at work
// (docs/DESIGN_SESSIONS_AI_SAVE.md "Modern AI walls").
const (
	breachFresh  = 90   // ticks the army's request stays current (it repeats it every think)
	breachEvery  = 1800 // ticks between constructors sent
	breachHold   = 1800 // ticks a breach constructor keeps its orders under threat
	breachRadius = 48   // the pieces reclaimed: the one asked for and its neighbours
	breachKeep   = 2    // constructors that never go
	breachLapse  = 450  // ticks without a request before the constructor out is called off
)

// breachState is the economy's record of the constructors it sent.
type breachState struct {
	last           uint32 // tick the last one was sent
	sent, recalled int64
}

// breach sends a constructor to a wall the army is held at; it returns the
// actions used.
func (e *Economy) breach(b *core.Board, budget int) int {
	s := e.s
	o := b.O
	if budget <= 0 || b.BreachTick == 0 {
		return 0
	}
	fresh := b.Tick-b.BreachTick <= breachFresh
	for _, bi := range b.Builders {
		u := &o.Own[bi]
		c := s.commitOf(u)
		if !s.breaching(u, c) {
			continue
		}
		if b.Tick-b.BreachTick <= breachLapse {
			return 0 // one is out already
		}
		e.one[0] = u.H
		b.K.Stop(e.one[:])
		*c = commitment{def: u.Info, gen: u.Gen, spot: -1, tick: s.tick}
		e.br.recalled++
		return 1
	}
	if !fresh || (e.br.last != 0 && s.tick-e.br.last < breachEvery) {
		return 0
	}
	pick, cons := int32(-1), 0
	var pickV, pickT int64
	for _, bi := range b.Builders {
		u := &o.Own[bi]
		info := u.Info
		if info.Role.Has(aikit.RoleCommander) || info.Def == nil || !info.Def.CanReclamate {
			continue
		}
		cons++
		v := int64(info.Value)
		t := int64(aikit.Dist(u.X, u.Z, b.BreachX, b.BreachZ)) / speedOf(info)
		if pick < 0 || v < pickV || (v == pickV && t < pickT) {
			pick, pickV, pickT = bi, v, t
		}
	}
	if pick < 0 || cons <= breachKeep {
		return 0
	}
	u := &o.Own[pick]
	b.K.Breach(u.H, b.BreachX, b.BreachZ, breachRadius)
	e.br.last = s.tick
	e.br.sent++
	*s.commitOf(u) = commitment{def: u.Info, gen: u.Gen, kind: cBreach, spot: -1, x: b.BreachX, z: b.BreachZ, tick: s.tick}
	d := e.record(u)
	d.note = noteAssign
	d.top[0] = cand{kind: cBreach, x: b.BreachX, z: b.BreachZ, score: 1, f: [4]int64{pickV, pickT, int64(cons), 0}}
	d.n, d.chosen = 1, 0
	return 1
}

// breaching reports whether a builder is out on a breach it must not leave
// for a threat.
func (s *shared) breaching(u *aikit.OwnUnit, c *commitment) bool {
	return c.kind == cBreach && s.tick-c.tick < breachHold && u.Order != aikit.OrderIdle
}

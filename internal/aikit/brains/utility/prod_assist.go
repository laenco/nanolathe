package utility

import (
	"github.com/nanolathe-gg/nanolathe/internal/aikit"
	"github.com/nanolathe-gg/nanolathe/internal/aikit/core"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
)

// Assist builders without products (breadth's assist part, prod_breadth.go)
// take no builder role, so neither the economy, which commands builders,
// nor the army, which commands armed units, ever orders them. Production
// does: an idle one, or one whose factory has stopped producing, guards a
// producing factory — the one the fewest of them guard, the nearest of those
// (guarding a factory helps it build, as the economy's guard assignment
// does) — else helps the nearest unfinished building of ours within
// assistReach.

// assistMem is the help last ordered for one assist builder, by handle.
type assistMem struct {
	def    *aikit.UnitInfo
	gen    uint32
	target pool.Handle
	tick   uint32
	guard  bool // target is a factory it guards (else a frame it helps)
}

// assistReach is how far an assist builder looks for a frame to help when
// no factory is producing: the economy's assist radius (economy.go
// evalAssist).
const assistReach = 1500

// commandAssists orders assist builders without products, one action each,
// within budget. It returns the actions used.
func (pr *Production) commandAssists(b *core.Board, budget int) int {
	br := &pr.br
	s := pr.s
	if !s.bpart(bAssist) || !br.init || !br.anyFree {
		return 0
	}
	o := b.O
	// How many of them guard each factory now (per b.Factories entry).
	br.guards = br.guards[:0]
	for range b.Factories {
		br.guards = append(br.guards, 0)
	}
	for i := range o.Own {
		u := &o.Own[i]
		if !u.Built || br.kind[u.Info.Index]&kAssistFree == 0 || int(u.H) >= len(br.mem) {
			continue
		}
		m := &br.mem[u.H]
		if m.def != u.Info || m.gen != u.Gen || !m.guard {
			continue
		}
		for j, fi := range b.Factories {
			if o.Own[fi].H == m.target {
				br.guards[j]++
				break
			}
		}
	}
	used := 0
	for i := range o.Own {
		if used >= budget {
			break
		}
		u := &o.Own[i]
		if !u.Built || br.kind[u.Info.Index]&kAssistFree == 0 {
			continue
		}
		br.mem = handleSlots(br.mem, u.H, s.hcap)
		m := &br.mem[u.H]
		if m.def != u.Info || m.gen != u.Gen {
			*m = assistMem{def: u.Info, gen: u.Gen}
		}
		if u.Order != aikit.OrderIdle && m.tick != 0 && (s.tick-m.tick < reassessTicks || pr.helping(b, m)) {
			continue
		}
		f, frame := pr.assistTarget(b, u, m)
		if f >= 0 && !canGuard(u.Info) {
			f = -1 // refuses Guard: help frames only
		}
		var one [1]pool.Handle
		one[0] = u.H
		switch {
		case f >= 0:
			t := &o.Own[b.Factories[f]]
			if m.guard && m.target == t.H && u.Order != aikit.OrderIdle {
				m.tick = s.tick
				continue // already guarding it
			}
			b.K.Guard(one[:], t.H, false)
			br.guards[f]++
			*m = assistMem{def: u.Info, gen: u.Gen, target: t.H, tick: s.tick, guard: true}
		case frame >= 0:
			t := &o.Own[frame]
			if !m.guard && m.target == t.H && u.Order != aikit.OrderIdle {
				m.tick = s.tick
				continue
			}
			b.K.Repair(one[:], t.H, false)
			*m = assistMem{def: u.Info, gen: u.Gen, target: t.H, tick: s.tick}
		default:
			m.tick = s.tick
			continue
		}
		br.assists++
		used++
	}
	return used
}

// helping reports whether an assist builder's last target still wants its
// help: a factory still producing, or a frame still unfinished.
func (pr *Production) helping(b *core.Board, m *assistMem) bool {
	i := b.Index(m.target)
	if i < 0 {
		return false
	}
	t := &b.O.Own[i]
	if m.guard {
		return t.QueueLen > 0 && !pr.s.fstateOf(t).blocked
	}
	return !t.Built
}

// assistTarget picks, for an assist builder, a producing factory (index
// into b.Factories, -1 for none): the one the fewest assist builders guard,
// counting this builder's own guard as none, the nearest of those; or,
// without one, the nearest frame of a building of ours within assistReach
// (O.Own index, -1 for none).
func (pr *Production) assistTarget(b *core.Board, u *aikit.OwnUnit, m *assistMem) (int32, int32) {
	s := pr.s
	o := b.O
	fac, frame := int32(-1), int32(-1)
	var bestF, bestB int64 = -1, -1
	var bestN int32
	for j, fi := range b.Factories {
		f := &o.Own[fi]
		if f.QueueLen == 0 || s.fstateOf(f).blocked {
			continue
		}
		n := pr.br.guards[j]
		if m.guard && m.target == f.H && n > 0 {
			n-- // its own guard
		}
		d := aikit.Dist2(u.X, u.Z, f.X, f.Z)
		if bestF < 0 || n < bestN || n == bestN && d < bestF {
			bestF, bestN, fac = d, n, int32(j)
		}
	}
	if fac >= 0 {
		return fac, -1
	}
	for _, i := range b.Frames {
		f := &o.Own[i]
		if f.Info.Role.Has(aikit.RoleMobile) || f.Progress >= 95 {
			continue
		}
		d := aikit.Dist2(u.X, u.Z, f.X, f.Z)
		if d > assistReach*assistReach {
			continue
		}
		if bestB < 0 || d < bestB {
			bestB, frame = d, i
		}
	}
	return -1, frame
}

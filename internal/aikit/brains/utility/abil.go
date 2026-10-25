package utility

import (
	"github.com/nanolathe-gg/nanolathe/internal/aikit/core"
)

// Abilities after building (docs/DESIGN_SESSIONS_AI_SAVE.md "Modern AI
// commands" and "Modern AI gates"; README §14): the units
// the economy has placed or production has made are used for what their
// definitions let them do beyond moving and shooting. Cloak hides a unit
// that is threatened while the energy store can pay for it (abil_cloak.go),
// and a gate network carries the army from the base to the front
// (abil_gates.go). Every choice is rebuilt from the observation each think,
// so a load rebuilds it; the little state kept between thinks (when a unit
// was last threatened, a gate's dial, the units sent through a gate) is
// keyed by handle and instance and only damps decisions.
type abilities struct {
	cloak cloakState
	gates gateState
}

// abilities runs the ability policies after the builders' retreats, with at
// most half of the economy's remaining actions (rounded up), and returns
// the actions it used. Each policy emits only when something changes, so
// most thinks use none.
func (e *Economy) abilities(b *core.Board, budget int) int {
	if budget <= 0 {
		return 0
	}
	limit := (budget + 1) / 2
	used := 0
	if e.s.p.Gates != 0 {
		used += e.serviceGates(b, limit-used)
	}
	if e.s.p.Cloak != 0 && used < limit {
		used += e.serviceCloak(b, limit-used)
	}
	return used
}

// Report publishes the ability counters to the arena (host-side, after the
// match): the most gates standing at once, dials, units sent through and
// carried, whether the gates were found not to carry, and the cloak
// requests made and cleared.
func (e *Economy) Report(add func(name string, value int64)) {
	g, c := &e.ab.gates, &e.ab.cloak
	if g.defs > 0 {
		add("gates_built", int64(g.peakBuilt))
		add("gate_dials", int64(g.dials))
		add("gate_sent", int64(g.sent))
		add("gate_carried", int64(g.delivered))
		if g.failed {
			add("gate_failed", 1)
		}
		for i, n := range g.why {
			if n != 0 {
				add("gate_eval_"+gateWhyNames[i], int64(n))
			}
		}
		for i, n := range g.front {
			if n != 0 {
				add("gate_front_"+frontWhyNames[i], int64(n))
			}
		}
	}
	add("cloak_on", int64(c.sentOn))
	add("cloak_off", int64(c.sentOff))
	add("cloak_peak", int64(c.peak))
	add("breach_sent", e.br.sent)
	add("breach_recalled", e.br.recalled)
	add("wall_segments", e.wl.segments)
	add("wall_pieces", e.wl.pieces)
	for i, n := range e.wl.why {
		if n != 0 {
			add("wall_why_"+wallWhyNames[i], n)
		}
	}
}

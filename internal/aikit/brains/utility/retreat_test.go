package utility

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/aikit"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
)

// An Easy commander caught away from home by an enemy force (four
// commanders here) that has also reached the base retreats with the think's only action: the army's and the idle
// factory's reserves come after it.
func TestCommanderRetreatsBeforeReserves(t *testing.T) {
	w := newMetalWorld(DefaultParams(), aikit.PersonaEasy)
	base := w.metalBase()
	w.thinkWith(25*1800, base, metalAmpleRes, energyFullRes)

	base[0].X, base[0].Z = 1250, 1050 // the commander, out reclaiming
	var mem []aikit.Remembered
	for i := 0; i < 4; i++ {
		mem = append(mem, aikit.Remembered{H: pool.Handle(100 + i), Gen: 1, Info: w.d.com, Owner: 1,
			X: 1300 + int32(i)*20, Z: 1080, LastSeen: 25*1800 + 90})
	}
	tick := uint32(25*1800 + 90)
	w.e.s.tokens, w.e.s.lastFill = 1000, tick // one action this think
	w.obs = &aikit.Obs{Tick: tick, Own: base, Memory: mem, Metal: metalAmpleRes, Energy: energyFullRes}
	w.k.Tick = tick
	w.b.Update(w.k, w.obs)
	if w.b.NearHomeThreat == 0 {
		t.Fatal("fixture: the raiders are not near home")
	}
	w.st.Plan(w.b)
	if w.e.s.budget != 1 {
		t.Fatalf("fixture: budget = %d, want 1", w.e.s.budget)
	}
	w.e.Plan(w.b)
	if c := w.e.s.commitOf(&w.obs.Own[0]); c.kind != cRetreat {
		t.Fatalf("commander commitment = %s, want a retreat", commitNames[c.kind])
	}
}

// The commander takes no work away from home where its retreat test
// already holds, but still works at home under the same threat.
func TestCommanderAvoidsThreatenedSites(t *testing.T) {
	w := newMetalWorld(DefaultParams(), aikit.PersonaEasy)
	var mem []aikit.Remembered
	for i := 0; i < 4; i++ {
		mem = append(mem, aikit.Remembered{H: pool.Handle(100 + i), Gen: 1, Info: w.d.com, Owner: 1,
			X: 900 + int32(i)*20, Z: 700, LastSeen: 25 * 1800})
	}
	w.obs = &aikit.Obs{Tick: 25 * 1800, Own: w.metalBase(), Memory: mem, Metal: metalAmpleRes, Energy: energyFullRes}
	w.k.Tick = w.obs.Tick
	w.b.Update(w.k, w.obs)
	w.st.Plan(w.b)
	s := w.e.s
	if !s.comOff(w.b, 900, 720) {
		t.Fatal("a site beside the enemy force was offered to the commander")
	}
	if s.comOff(w.b, 600, 560) {
		t.Fatal("a site at home was refused under the same threat")
	}
	if s.comOff(w.b, 300, 1000) {
		t.Fatal("a quiet site away from home was refused")
	}
	if !s.comOff(w.b, 1800, 1800) {
		t.Fatal("a site beyond the leash was offered")
	}
}

// At a start with no metal inside com_radius the commander's leash reaches
// the second-nearest land spot, so the opening's only builder has
// extractors to build; spots beyond that stay off its list.
func TestCommanderLeashReachesMetal(t *testing.T) {
	w := newMetalWorld(DefaultParams(), aikit.PersonaEasy)
	m := w.k.Map
	m.Spots = m.Spots[:0]
	for _, xz := range [][2]int32{{1500, 512}, {512, 1520}, {1700, 1700}} {
		m.Spots = append(m.Spots, aikit.MetalSpot{CellX: xz[0]/16 - 1, CellZ: xz[1]/16 - 1, X: xz[0], Z: xz[1], Metal: 400})
	}
	w.thinkWith(1800, []aikit.OwnUnit{own(1, w.d.com, 512, 512)}, metalAmpleRes, energyFullRes)
	s := w.e.s
	if s.comOff(w.b, 1500, 512) || s.comOff(w.b, 512, 1520) {
		t.Fatalf("leash %d does not reach the two nearest spots", s.comLeash())
	}
	if !s.comOff(w.b, 1700, 1700) {
		t.Fatalf("leash %d reaches the third spot", s.comLeash())
	}
}

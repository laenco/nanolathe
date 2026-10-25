package tactics

import (
	"github.com/nanolathe-gg/nanolathe/internal/aikit"
	"github.com/nanolathe-gg/nanolathe/internal/aikit/core"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
)

// Production breadth (the utility brain's breadth parameter;
// docs/DESIGN_SESSIONS_AI_SAVE.md "Modern AI production variety") reaches
// the army in two ways, and neither needs a switch here: without breadth
// the utility brain makes neither more than scoutsMax cheap fast units in a
// game nor any support unit, so the army plays as before.
//
//   - Cheap fast combat units are produced beyond the scout allowance. The
//     first scoutsMax of a game scout; later ones are ordinary army units
//     (assign gives a fast one to the raiders).
//   - Mobile radar and radar jammers carry no weapon, so they are no squad's
//     members and nothing else orders them. Each guards — follows — the
//     main squad's ground member nearest its centre, and waits at the
//     gather point while the main squad has none.
const (
	tagSupport int32 = 23 // a mobile radar or jammer kept with the main squad
	// scoutsMax bounds the scouts over a game, as production's maxScouts
	// does: a scout that keeps dying is not finding anything new.
	scoutsMax = 3
	// supportStray is how far a support unit's ward may be from the main
	// squad's centre before the support unit is given a member nearer it
	// (the squad's body radius for reinforcements, presentRadius, plus a
	// margin so a ward at the edge is not swapped every think).
	supportStray = 900
	// supportRetry is the least time between two orders to one support
	// unit, so a ward that keeps changing does not spend the budget.
	supportRetry = 150
)

// SupportUnit reports a mobile radar or radar jammer the army keeps with
// the main squad: a ground, hover or amphibious unit, unarmed, without
// build power, with a radar reaching past its sight or a radar jammer, and
// nothing else a layer uses it for; a definition that costs no metal or has
// a single hit point is a scripted helper. It is the utility brain's
// supportKind test, which produces these (TestSupportKindsAgree).
func SupportUnit(u *aikit.UnitInfo) bool {
	r := u.Role
	if u.Def == nil || !r.Has(aikit.RoleMobile) || r.Any(aikit.RoleAir|aikit.RoleNaval|aikit.RoleCommander|aikit.RoleBuilder|aikit.RoleCombat|
		aikit.RoleTransport|aikit.RoleKamikaze|aikit.RoleStockpile|aikit.RoleInterceptor|aikit.RoleUpgrade) {
		return false
	}
	if u.DPS+u.AirDPS > 0 || u.BuildPower > 0 || u.Metal <= 0 || u.HP <= 1 {
		return false
	}
	return u.Def.RadarDistance > u.Def.SightDistance || u.Def.RadarDistanceJam > 0
}

// supportMem is what the army last ordered one support unit, by handle.
type supportMem struct {
	info    *aikit.UnitInfo
	gen     uint32
	ward    pool.Handle // the member it guards (0: none)
	wardGen uint32
	tick    uint32
}

func (a *Army) supportOf(u *aikit.OwnUnit) *supportMem {
	h := int(u.H)
	for len(a.sup) <= h {
		a.sup = append(a.sup, supportMem{})
	}
	m := &a.sup[h]
	if m.info != u.Info || m.gen != u.Gen {
		*m = supportMem{info: u.Info, gen: u.Gen}
	}
	return m
}

// support keeps the support units with the main squad (one guard order
// each, when its ward changes) or at the gather point.
func (a *Army) support(b *core.Board) {
	o := b.O
	k := b.K
	main := &a.sq[sqMain]
	ward := int32(-1)
	if main.hasCentre {
		var best int64 = -1
		for _, i := range main.members {
			u := &o.Own[i]
			if c := a.cls(u.Info); c.air() || c.kind == ukNaval {
				continue
			}
			if d := aikit.Dist2(u.X, u.Z, main.cx, main.cz); best < 0 || d < best {
				best, ward = d, i
			}
		}
	}
	var one [1]pool.Handle
	for i := range o.Own {
		u := &o.Own[i]
		if !u.Built || !SupportUnit(u.Info) {
			continue
		}
		if u.Tag != tagSupport {
			k.SetTag(u.H, tagSupport)
		}
		m := a.supportOf(u)
		if b.Tick-m.tick < supportRetry && m.tick != 0 {
			continue
		}
		one[0] = u.H
		if ward >= 0 && !u.Info.Def.CanGuard {
			// A definition authored canguard=0 refuses Guard (code 7): it
			// follows the main squad by moving to its ward once it has
			// stopped away from it, instead of a refused order every retry.
			w := &o.Own[ward]
			if u.Order != aikit.OrderIdle || aikit.Dist2(u.X, u.Z, w.X, w.Z) <= supportStray*supportStray {
				continue
			}
			if !a.spend(false) {
				return
			}
			k.Move(one[:], w.X, w.Z, false)
			m.ward, m.wardGen, m.tick = w.H, w.Gen, b.Tick
			a.supportOrders++
			continue
		}
		if ward >= 0 {
			if u.Order == aikit.OrderGuard && a.keepsWard(b, main, m) {
				continue
			}
			if !a.spend(false) {
				return
			}
			w := &o.Own[ward]
			k.Guard(one[:], w.H, false)
			m.ward, m.wardGen, m.tick = w.H, w.Gen, b.Tick
			a.supportOrders++
			continue
		}
		// No main squad to follow: wait at the gather point.
		if !a.gatherSet || (u.Order != aikit.OrderIdle && m.ward == 0) || aikit.Dist2(u.X, u.Z, a.gx, a.gz) <= 400*400 {
			continue
		}
		if !a.spend(false) {
			return
		}
		k.Move(one[:], a.gx, a.gz, false)
		m.ward, m.wardGen, m.tick = 0, 0, b.Tick
		a.supportOrders++
	}
}

// keepsWard reports whether a support unit's ward is still a main-squad
// member near the squad's centre.
func (a *Army) keepsWard(b *core.Board, main *squad, m *supportMem) bool {
	if m.ward == 0 {
		return false
	}
	wi := b.Index(m.ward)
	if wi < 0 || b.O.Own[wi].Gen != m.wardGen {
		return false
	}
	w := &b.O.Own[wi]
	if aikit.Dist2(w.X, w.Z, main.cx, main.cz) > supportStray*supportStray {
		return false
	}
	for _, i := range main.members {
		if i == wi {
			return true
		}
	}
	return false
}

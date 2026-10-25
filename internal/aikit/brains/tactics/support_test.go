package tactics

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/aikit"
	"github.com/nanolathe-gg/nanolathe/internal/aikit/core"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
)

// supportFixture is a board of a radar unit and the given main-squad
// tanks, updated as a think does; the main squad's centre is (1000, 1000).
func supportFixture(t *testing.T, tanks int) (*Army, *core.Board, *aikit.UnitInfo) {
	t.Helper()
	a, m := testArmy(40, 40)
	tank := &aikit.UnitInfo{Index: 0, Role: aikit.RoleMobile | aikit.RoleCombat, DPS: 50, HP: 1000, Value: 200, Speed: 40, Def: &content.UnitDef{}}
	radar := &aikit.UnitInfo{Index: 1, Role: aikit.RoleMobile | aikit.RoleScout | aikit.RoleRadar, Metal: 90, HP: 400, Speed: 30,
		Def: &content.UnitDef{RadarDistance: 1200, SightDistance: 300, CanGuard: true}}
	a.classes = []uclass{{kind: ukGround}, {kind: ukGround}}
	own := []aikit.OwnUnit{{Info: radar, X: 300, Z: 300, Built: true}}
	for i := 0; i < tanks; i++ {
		own = append(own, aikit.OwnUnit{Info: tank, X: 1000 + int32(i)*40, Z: 1000, Built: true, Tag: sqMain})
	}
	for i := range own {
		own[i].H, own[i].Gen = pool.Handle(10+i), 1
	}
	k := &aikit.Kit{Persona: aikit.PersonaHard, Map: m}
	b := &core.Board{}
	b.Update(k, &aikit.Obs{Tick: 900, Own: own})
	s := &a.sq[sqMain]
	s.members = s.members[:0]
	for i := 1; i < len(own); i++ {
		s.members = append(s.members, int32(i))
	}
	s.cx, s.cz, s.hasCentre = 1000, 1000, tanks > 0
	a.avail = 1 << 20
	return a, b, radar
}

// A mobile radar guards the main squad's member nearest its centre, keeps
// that ward while it stays near the centre, and waits at the gather point
// while the main squad has no ground member.
func TestSupportFollowsMainSquad(t *testing.T) {
	a, b, _ := supportFixture(t, 3)
	a.support(b)
	m := a.sup[b.O.Own[0].H]
	if a.supportOrders != 1 || m.ward != b.O.Own[1].H {
		t.Fatalf("orders %d, ward %d: want one guard on the member at the centre (%d)", a.supportOrders, m.ward, b.O.Own[1].H)
	}
	b.O.Own[0].Order = aikit.OrderGuard
	b.Tick += supportRetry
	a.support(b)
	if a.supportOrders != 1 {
		t.Errorf("ward unchanged: %d orders, want no new one", a.supportOrders)
	}
	a, b, _ = supportFixture(t, 0)
	a.gx, a.gz, a.gatherSet = 2000, 2000, true
	a.support(b)
	if a.supportOrders != 1 || a.sup[b.O.Own[0].H].ward != 0 {
		t.Errorf("no main squad: %d orders, want one move to the gather point", a.supportOrders)
	}
}

// A support unit authored canguard=0 (TLL's TLLCONFUSER) refuses Guard, so
// it follows its ward by moving: once when it stands idle out of reach, and
// not again while it is under way or idle near the ward.
func TestSupportWithoutGuardFollowsByMoving(t *testing.T) {
	a, b, radar := supportFixture(t, 3)
	radar.Def.CanGuard = false
	b.O.Own[0].Order = aikit.OrderMove
	a.support(b)
	if a.supportOrders != 0 {
		t.Fatalf("under way: %d orders, want none (a guarding radar would be re-sent)", a.supportOrders)
	}
	b.O.Own[0].Order = aikit.OrderIdle
	a.support(b)
	if m := a.sup[b.O.Own[0].H]; a.supportOrders != 1 || m.ward != b.O.Own[1].H {
		t.Fatalf("idle and far: %d orders, ward %d; want one move to %d", a.supportOrders, m.ward, b.O.Own[1].H)
	}
	b.O.Own[0].X, b.O.Own[0].Z = 1000, 1100
	b.Tick += supportRetry
	a.support(b)
	if a.supportOrders != 1 {
		t.Errorf("idle near its ward: %d orders, want no new one", a.supportOrders)
	}
}

// The first scoutsMax cheap fast units of a game scout; later ones join
// the squads.
func TestScoutsAreCapped(t *testing.T) {
	a, m := testArmy(40, 40)
	a.P.Naval, a.P.Air = false, false
	fast := &aikit.UnitInfo{Index: 0, Role: aikit.RoleMobile | aikit.RoleCombat | aikit.RoleScout, DPS: 20, HP: 100, Value: 50, Speed: 90, Def: &content.UnitDef{}}
	a.classes = []uclass{{kind: ukGround}}
	var own []aikit.OwnUnit
	for i := 0; i < scoutsMax+2; i++ {
		own = append(own, aikit.OwnUnit{H: pool.Handle(5 + i), Gen: 1, Info: fast, X: 500, Z: 500, Built: true})
	}
	k := &aikit.Kit{Persona: aikit.PersonaHard, Map: m}
	b := &core.Board{}
	b.Update(k, &aikit.Obs{Tick: 900, Own: own})
	a.sq[sqMain].active = true
	a.membership(b)
	var army int
	for id := range a.sq {
		army += len(a.sq[id].members)
	}
	if army != 2 || a.scoutsEver != scoutsMax {
		t.Errorf("%d in squads, %d made scouts; want 2 and %d", army, a.scoutsEver, scoutsMax)
	}
}

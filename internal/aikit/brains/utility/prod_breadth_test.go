package utility

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/aikit"
	"github.com/nanolathe-gg/nanolathe/internal/aikit/brains/tactics"
	"github.com/nanolathe-gg/nanolathe/internal/aikit/core"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
)

// breadthFixture is a lab making a constructor, three combat units of
// falling efficiency, a cheap fast unit, a mobile radar, a jammer, an
// assist builder and two units with build power for other purposes (a
// resurrection unit, a minelayer), with a production layer over them.
type breadthFixture struct {
	pr                                *Production
	k                                 *aikit.Kit
	con, heavy, mid, weak, fast       *aikit.UnitInfo
	radar, jam, fark, necro, mlv, lab *aikit.UnitInfo
}

func newBreadthFixture(p Params) *breadthFixture {
	f := &breadthFixture{}
	mob := aikit.RoleMobile
	mex := &aikit.UnitInfo{Key: "mex", Role: aikit.RoleExtractor, Metal: 50, Def: &content.UnitDef{}}
	mine := &aikit.UnitInfo{Key: "mine", Metal: 10, HP: 10, Def: &content.UnitDef{}}
	f.con = &aikit.UnitInfo{Key: "con", Role: mob | aikit.RoleBuilder, Metal: 100, Energy: 1100, HP: 700, BuildPower: 100, Speed: 30,
		Builds: []*aikit.UnitInfo{mex}, Def: &content.UnitDef{Builder: true}}
	combat := func(key string, dps, hp, metal int32) *aikit.UnitInfo {
		return &aikit.UnitInfo{Key: key, Role: mob | aikit.RoleCombat, DPS: dps, HP: hp, Metal: metal, Value: metal, Speed: 40, Def: &content.UnitDef{}}
	}
	f.heavy, f.mid, f.weak = combat("heavy", 50, 1000, 200), combat("mid", 30, 800, 200), combat("weak", 5, 300, 200)
	f.fast = combat("fast", 40, 200, 50)
	f.fast.Role |= aikit.RoleScout
	f.fast.Speed = 90
	f.radar = &aikit.UnitInfo{Key: "radar", Role: mob | aikit.RoleScout | aikit.RoleRadar, Metal: 90, HP: 400, Speed: 30,
		Def: &content.UnitDef{RadarDistance: 1200, SightDistance: 300}}
	f.jam = &aikit.UnitInfo{Key: "jam", Role: mob | aikit.RoleScout | aikit.RoleJammer, Metal: 90, HP: 400, Speed: 30,
		Def: &content.UnitDef{RadarDistanceJam: 400, SightDistance: 300}}
	f.fark = &aikit.UnitInfo{Key: "fark", Role: mob, Metal: 300, Energy: 1000, HP: 800, BuildPower: 180, Speed: 60, Def: &content.UnitDef{Builder: true, CanGuard: true}}
	f.necro = &aikit.UnitInfo{Key: "necro", Role: mob, Metal: 300, Energy: 1000, HP: 800, BuildPower: 180, Speed: 40, Def: &content.UnitDef{Builder: true, CanResurrect: true}}
	f.mlv = &aikit.UnitInfo{Key: "mlv", Role: mob | aikit.RoleBuilder, Metal: 170, HP: 1200, BuildPower: 100, Speed: 26,
		Builds: []*aikit.UnitInfo{mine}, Def: &content.UnitDef{Builder: true}}
	f.lab = &aikit.UnitInfo{Key: "lab", Role: aikit.RoleFactory, Metal: 600, HP: 3000, BuildPower: 100, FootX: 6, FootZ: 6, Def: &content.UnitDef{},
		Builds: []*aikit.UnitInfo{f.con, f.heavy, f.mid, f.weak, f.fast, f.radar, f.jam, f.fark, f.necro, f.mlv, f.heavy}}
	tab := &aikit.Table{Units: []*aikit.UnitInfo{f.lab, f.con, mex, mine, f.heavy, f.mid, f.weak, f.fast, f.radar, f.jam, f.fark, f.necro, f.mlv}}
	for i, u := range tab.Units {
		u.Index = int32(i)
		u.Side = "ARM"
	}
	f.k = &aikit.Kit{Side: "ARM", Table: tab, Map: &aikit.MapInfo{WorldW: 8192, WorldH: 8192, SectorW: 64, SectorH: 64}, Persona: aikit.PersonaHard}
	s := &shared{p: p}
	s.setup(f.k)
	f.pr = &Production{s: s}
	return f
}

// Breadth's classes come from authored keys and roles: a cheap fast unit
// is a combat product, a radar reaching past its sight and a radar jammer
// are support, a unit with build power and no products is an assist
// builder unless it authors resurrection, and a builder whose only
// products are buildings the economy never places is not one. Each lab
// lists its combat products once, its cheapest support of each kind and
// its best assist builder.
func TestBreadthClasses(t *testing.T) {
	f := newBreadthFixture(DefaultParams())
	f.pr.setupBreadth(f.k, false)
	br := &f.pr.br
	for _, c := range []struct {
		u    *aikit.UnitInfo
		want uint8
	}{
		{f.con, 0}, {f.heavy, kCombat}, {f.weak, kCombat}, {f.fast, kCombat | kFast},
		{f.radar, kRadar}, {f.jam, kJam}, {f.fark, kAssistFree}, {f.necro, 0}, {f.mlv, 0},
	} {
		if got := br.kind[c.u.Index]; got != c.want {
			t.Errorf("%s: classes %b, want %b", c.u.Key, got, c.want)
		}
	}
	fb := &br.fac[f.lab.Index]
	if len(fb.combat) != 4 || fb.radar != f.radar || fb.jam != f.jam || fb.assist != f.fark {
		t.Errorf("lab table: combat %v radar %v jam %v assist %v", fb.combat, fb.radar, fb.jam, fb.assist)
	}
	if br.pref != nil || br.seedSet {
		t.Error("a preference without a draw")
	}
}

// The support predicate production uses is the one the tactics army keeps
// with the main squad.
func TestSupportKindsAgree(t *testing.T) {
	f := newBreadthFixture(DefaultParams())
	odd := []*aikit.UnitInfo{
		{Role: aikit.RoleMobile | aikit.RoleRadar, Metal: 0, HP: 1, Def: &content.UnitDef{RadarDistance: 9000}},                       // a scripted helper
		{Role: aikit.RoleMobile | aikit.RoleRadar, Metal: 90, HP: 400, Def: &content.UnitDef{RadarDistance: 100, SightDistance: 300}}, // a token radar
		{Role: aikit.RoleMobile | aikit.RoleRadar | aikit.RoleAir, Metal: 90, HP: 400, Def: &content.UnitDef{RadarDistance: 1200}},    // a radar plane
		{Role: aikit.RoleMobile | aikit.RoleJammer | aikit.RoleNaval, Metal: 90, HP: 400, Def: &content.UnitDef{RadarDistanceJam: 400}},
		{Role: aikit.RoleMobile | aikit.RoleSonar, Metal: 90, HP: 400, Def: &content.UnitDef{SonarDistance: 400}},
		{Role: aikit.RoleMobile | aikit.RoleRadar | aikit.RoleJammer, Metal: 500, HP: 800, Def: &content.UnitDef{Builder: true, RadarDistance: 1200, RadarDistanceJam: 400, SightDistance: 300}},
	}
	all := append([]*aikit.UnitInfo{}, f.k.Table.Units...)
	for _, u := range append(all, odd...) {
		if (supportKind(u) != 0) != tactics.SupportUnit(u) {
			t.Errorf("%q %+v: production says %b, the army %v", u.Key, u.Role, supportKind(u), tactics.SupportUnit(u))
		}
	}
	if supportKind(odd[5]) != kRadar|kJam || supportKind(odd[1]) != 0 || supportKind(odd[0]) != 0 {
		t.Error("a radar jammer is both kinds; a token radar and a scripted helper are neither")
	}
}

// With jitter the preference is one draw from the player's generator, in
// production's Init, which core runs after the strategy's: its earlier
// draws are untouched. A seed replays; the preferences lie in 1000 ± 250.
// Without jitter, or with breadth's variety off, nothing is drawn.
func TestPreferenceIsOneDraw(t *testing.T) {
	for _, c := range []struct {
		name    string
		breadth int32
		draw    bool
		drawn   bool
	}{{"jitter", 15, true, true}, {"no jitter", 15, false, false}, {"variety off", 14, true, false}} {
		p := DefaultParams()
		p.Breadth = c.breadth
		f := newBreadthFixture(p)
		r := aikit.PlayerRand(51, 1)
		f.k.Rand = &r
		f.pr.setupBreadth(f.k, c.draw)
		want := aikit.PlayerRand(51, 1)
		if c.drawn {
			want.Uint32()
		}
		if r != want || f.pr.br.seedSet != c.drawn || (f.pr.br.pref != nil) != c.drawn {
			t.Errorf("%s: generator moved %v, drawn %v", c.name, r != aikit.PlayerRand(51, 1), f.pr.br.seedSet)
		}
	}
	var lo, hi int32 = one, one
	distinct := map[int32]bool{}
	for i := int32(0); i < 400; i++ {
		v := prefFrom(7, i)
		lo, hi = min(lo, v), max(hi, v)
		distinct[v] = true
		if v != prefFrom(7, i) {
			t.Fatal("a preference did not replay")
		}
	}
	if lo < one-2*prefSpread || hi > one+2*prefSpread || len(distinct) < 100 {
		t.Errorf("preferences span %d..%d with %d values", lo, hi, len(distinct))
	}
	if prefFrom(7, 3) == prefFrom(8, 3) && prefFrom(7, 4) == prefFrom(8, 4) && prefFrom(7, 5) == prefFrom(8, 5) {
		t.Error("another seed gave the same preferences")
	}
}

// Variety: a product's share is counted among its factory's combat units,
// a unit far below the best is cut by the floor however the share stands,
// and the preference scales what is left.
func TestVarietyFloorAndPreference(t *testing.T) {
	f := newBreadthFixture(DefaultParams())
	pr, s := f.pr, f.pr.s
	pr.setupBreadth(f.k, false)
	s.count[f.heavy.Index], s.count[f.mid.Index] = 4, 1
	best := pr.bestEffN(f.lab, true)
	score := func(q *aikit.UnitInfo, facMix int64) int64 {
		sc, _ := pr.combatScoreN(q, best, 1000, one, one, facMix)
		return sc
	}
	if score(f.weak, 5) != 0 || score(f.weak, -1) == 0 {
		t.Errorf("weak unit (%d‰ of the best): %d with variety, %d without; want cut only with it",
			s.effN(f.weak, 0)*1000/best, score(f.weak, 5), score(f.weak, -1))
	}
	// Four of the factory's five are heavy: its share is 666‰, the mid
	// unit's (at 480‰ of its efficiency) 166‰, so the mid unit leads.
	if h, m := score(f.heavy, 5), score(f.mid, 5); m <= h {
		t.Errorf("heavy %d, mid %d: the factory's own mix should favour the mid unit", h, m)
	}
	if got, want := pr.mixOf(f.heavy, 5), half(666, mixHalf); got != want {
		t.Errorf("heavy's mix %d, want half(666, %d) = %d", got, mixHalf, want)
	}
	plain := score(f.mid, 5)
	pr.br.pref = make([]int32, len(f.k.Table.Units))
	for i := range pr.br.pref {
		pr.br.pref[i] = one
	}
	pr.br.pref[f.mid.Index] = 1250
	if got := score(f.mid, 5); got != plain*1250/1000 && got != plain*1250/1000-1 {
		t.Errorf("preference 1250: %d, want about %d", got, plain*1250/1000)
	}
}

// produceBoard puts the lab and the given units on a board, updated as a
// think does, with energy and metal to spare and no constructor wanted.
func (f *breadthFixture) produceBoard(own ...aikit.OwnUnit) *core.Board {
	s := f.pr.s
	s.tick += 30
	s.minutes = int32(s.tick / 1800)
	units := append([]aikit.OwnUnit{{Info: f.lab, X: 2000, Z: 2000, Built: true}}, own...)
	for i := range units {
		units[i].H, units[i].Gen = pool.Handle(1+i), 1
	}
	o := &aikit.Obs{Tick: s.tick, Own: units, Metal: aikit.Res{Stock: 500, Cap: 1000, Income: 30}, Energy: aikit.Res{Stock: 5000, Cap: 6000, Income: 500}}
	b := &core.Board{}
	b.Update(f.k, o)
	b.Posture.EcoShare = 40
	clear(s.count)
	for i := range units {
		s.count[units[i].Info.Index]++
	}
	s.covE, s.covM, s.scouts = 1500, 1500, 1
	s.cons = 30 // no constructor wanted: the economy is covered
	s.p.WCons = 0
	return b
}

// notOffered is offered's answer for a product outside the factory's last
// top candidates.
const notOffered commitKind = 255

// offered reports the kind a product was offered as in the factory's last
// decision.
func (f *breadthFixture) offered(q *aikit.UnitInfo) commitKind {
	pr := f.pr
	if pr.next == 0 {
		return notOffered
	}
	d := &pr.ring[(pr.next+len(pr.ring)-1)%len(pr.ring)]
	for i := 0; i < d.n; i++ {
		if d.top[i].prod == q {
			return d.top[i].kind
		}
	}
	return notOffered
}

// Production stops offering constructors once builders are already idle.
// The economy can still use the pool; another constructor only crowds the
// base and blocks factory exits.
func TestIdleBuildersStopConstructorProduction(t *testing.T) {
	f := newBreadthFixture(DefaultParams())
	b := f.produceBoard()
	f.pr.s.p.WCons = 100
	f.pr.s.cons = 0
	f.pr.s.freeSafe = 60
	f.pr.s.idleBuilders = 2
	f.pr.s.idleCons = [2]int32{2, 0}
	f.pr.produce(b, 4)
	if got := f.offered(f.con); got == pCons {
		t.Error("idle builders did not stop constructor production")
	}
	// The commander is an idle builder too, but not an idle economy
	// constructor: one idle commander and one idle constructor must not stop
	// the factory that would give them work.
	f.pr.s.idleBuilders, f.pr.s.idleCons = 2, [2]int32{}
	f.pr.produce(b, 4)
	if got := f.offered(f.con); got != pCons {
		t.Errorf("one idle constructor plus commander: constructor offered as %d, want constructor", got)
	}
	f.pr.s.idleBuilders, f.pr.s.idleCons = 0, [2]int32{}
	f.pr.s.idleCons = [2]int32{0, 2}
	f.pr.produce(b, 4)
	if got := f.offered(f.con); got != pCons {
		t.Errorf("two idle naval constructors: land constructor offered as %d, want constructor", got)
	}
	f.pr.s.idleCons = [2]int32{}
	f.pr.produce(b, 4)
	if got := f.offered(f.con); got != pCons {
		t.Errorf("no idle builders: constructor offered as %d, want constructor", got)
	}
}

// Beyond the scout allowance a cheap fast unit is a combat candidate
// while cheap fast units are under 30% of the factory's combat units, and
// never with the fast part off.
func TestFastUnitsJoinTheMix(t *testing.T) {
	heavies := func(f *breadthFixture, n int) []aikit.OwnUnit {
		var u []aikit.OwnUnit
		for i := 0; i < n; i++ {
			u = append(u, aikit.OwnUnit{Info: f.heavy, X: 2500, Z: 2500, Built: true})
		}
		return u
	}
	f := newBreadthFixture(DefaultParams())
	f.pr.produce(f.produceBoard(heavies(f, 8)...), 4)
	if got := f.offered(f.fast); got != pCombat {
		t.Fatalf("no cheap fast unit among 8 heavies: offered as %d, want combat", got)
	}
	own := heavies(f, 6)
	for i := 0; i < 4; i++ {
		own = append(own, aikit.OwnUnit{Info: f.fast, X: 2500, Z: 2500, Built: true})
	}
	f.pr.produce(f.produceBoard(own...), 4)
	if got := f.offered(f.fast); got == pCombat {
		t.Error("four of ten combat units are cheap fast ones: still offered")
	}
	p := DefaultParams()
	p.Breadth = 0
	f = newBreadthFixture(p)
	f.pr.produce(f.produceBoard(heavies(f, 8)...), 4)
	if got := f.offered(f.fast); got == pCombat {
		t.Error("breadth off: a cheap fast unit offered as combat")
	}
}

// Support units are wanted from an army of 1500, one more per 3000, two
// of each kind at most; the lab offers its radar until one is owned, and
// none while the base is threatened.
func TestSupportWithTheArmy(t *testing.T) {
	for _, c := range []struct{ army, want int64 }{{1499, 0}, {1500, 1}, {4499, 1}, {4500, 2}, {90000, 2}} {
		if got := supportWant(c.army); got != c.want {
			t.Errorf("army %d: %d wanted, want %d", c.army, got, c.want)
		}
	}
	f := newBreadthFixture(DefaultParams())
	var own []aikit.OwnUnit
	for i := 0; i < 10; i++ {
		own = append(own, aikit.OwnUnit{Info: f.heavy, X: 2500, Z: 2500, Built: true})
	}
	b := f.produceBoard(own...)
	f.pr.produce(b, 4)
	if f.offered(f.radar) != pSupport || f.offered(f.jam) != pSupport {
		t.Fatalf("army %d: radar offered as %d, jammer %d", b.ArmyValue, f.offered(f.radar), f.offered(f.jam))
	}
	b = f.produceBoard(append(own, aikit.OwnUnit{Info: f.radar, X: 2400, Z: 2400, Built: true})...)
	f.pr.produce(b, 4)
	if f.offered(f.radar) == pSupport || f.offered(f.jam) != pSupport {
		t.Errorf("one radar owned: radar offered as %d, jammer %d", f.offered(f.radar), f.offered(f.jam))
	}
}

// One assist builder per two live factories, at most four, while metal
// outruns what build power spends; one without products guards the
// producing factory the fewest of them guard, or helps a frame of ours
// when none is producing.
func TestAssistBuilders(t *testing.T) {
	for _, c := range []struct {
		facs int32
		want int64
	}{{0, 0}, {1, 1}, {2, 1}, {3, 2}, {8, 4}, {11, 4}} {
		if got := assistWant(c.facs); got != c.want {
			t.Errorf("%d factories: %d assist builders wanted, want %d", c.facs, got, c.want)
		}
	}
	f := newBreadthFixture(DefaultParams())
	var own []aikit.OwnUnit
	for i := 0; i < 4; i++ {
		own = append(own, aikit.OwnUnit{Info: f.heavy, X: 2500, Z: 2500, Built: true})
	}
	b := f.produceBoard(own...)
	f.pr.s.p.WCons = 100
	f.pr.s.covM = 2500
	f.pr.produce(b, 4)
	if f.offered(f.fark) != pAssist || f.offered(f.necro) != notOffered {
		t.Fatalf("assist builder offered as %d, the resurrection unit as %d", f.offered(f.fark), f.offered(f.necro))
	}
	f.pr.s.covM = 800
	f.pr.produce(b, 4)
	if f.offered(f.fark) == pAssist {
		t.Error("metal short of build power: an assist builder offered")
	}
	b = f.produceBoard(aikit.OwnUnit{Info: f.fark, X: 1000, Z: 1000, Built: true})
	f.pr.s.covM = 2500
	f.pr.produce(b, 4)
	if f.offered(f.fark) == pAssist {
		t.Error("one assist builder for one factory: another offered")
	}

	// Orders: the lab producing, then idle with a frame of ours to help.
	frame := aikit.OwnUnit{Info: f.lab, X: 1200, Z: 1000, Progress: 30}
	b = f.produceBoard(aikit.OwnUnit{Info: f.fark, X: 1000, Z: 1000, Built: true}, frame)
	b.O.Own[0].QueueLen = 1
	if n := f.pr.commandAssists(b, 4); n != 1 || !f.pr.br.mem[b.O.Own[1].H].guard || f.pr.br.mem[b.O.Own[1].H].target != b.O.Own[0].H {
		t.Fatalf("producing lab: %d orders, memory %+v", n, f.pr.br.mem[b.O.Own[1].H])
	}
	f.pr.s.tick += reassessTicks
	b = f.produceBoard(aikit.OwnUnit{Info: f.fark, X: 1000, Z: 1000, Built: true}, frame)
	b.O.Own[1].Order = aikit.OrderGuard
	if n := f.pr.commandAssists(b, 4); n != 1 || f.pr.br.mem[b.O.Own[1].H].guard || f.pr.br.mem[b.O.Own[1].H].target != b.O.Own[2].H {
		t.Errorf("idle lab: %d orders, memory %+v; want help for the frame", n, f.pr.br.mem[b.O.Own[1].H])
	}
}

// A persona short of actions queues combat units two at a time; builders
// and every product of a persona with actions to spare stay one at a time.
func TestLowAPMCombatBatch(t *testing.T) {
	q := &aikit.UnitInfo{Key: "tank"}
	for _, tc := range []struct {
		persona aikit.Persona
		kind    commitKind
		want    int32
	}{
		{aikit.PersonaEasy, pCombat, 2},
		{aikit.PersonaMed, pCombat, 2},
		{aikit.PersonaHard, pCombat, 1},
		{aikit.PersonaEasy, pCons, 1},
		{aikit.PersonaEasy, pScout, 1},
	} {
		k := &aikit.Kit{Persona: tc.persona}
		if got := batchOf(k, &cand{kind: tc.kind, prod: q}); got != tc.want {
			t.Errorf("%s kind %d: batch %d, want %d", tc.persona.Name, tc.kind, got, tc.want)
		}
	}
}

package tactics

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/aikit"
	"github.com/nanolathe-gg/nanolathe/internal/aikit/core"
	"github.com/nanolathe-gg/nanolathe/internal/content"
)

// A wall piece takes damage only from a burst that reaches its centre from
// its face: half the area of effect must reach 16 wu [06 §9.3][06 §13.1].
// Rocket-sized bursts count in full, light cannons at half, small arms not
// at all.
func TestWallDamageFollowsTheBurst(t *testing.T) {
	for _, tc := range []struct {
		aoe  int32
		want int32
	}{{8, 0}, {30, 0}, {32, 15}, {48, 30}} {
		w := &content.WeaponDef{ID: 1, DamageDefault: 30, ReloadTime: 30, AreaOfEffect: tc.aoe, Range: 200}
		u := &aikit.UnitInfo{Role: aikit.RoleMobile | aikit.RoleCombat, DPS: 30, Def: &content.UnitDef{CanMove: true, BMCode: 1, Weapon1Def: w}}
		if got := classOf(u, 0).wallDPS; got != tc.want {
			t.Errorf("area of effect %d: wall damage %d per second, want %d", tc.aoe, got, tc.want)
		}
	}
}

// The piece a held squad breaks is the nearest one ahead of it, unless the
// enemy's static fire covers that one and not a neighbour; our own walls
// and pieces behind the squad are never chosen.
func TestBreachPieceAvoidsTowerFire(t *testing.T) {
	building := &aikit.UnitInfo{Role: aikit.RoleEnergy}
	for _, tc := range []struct {
		name     string
		covered  bool
		own      bool
		wantX    int32
		wantNone bool
	}{
		{name: "nearest", wantX: 2000},
		{name: "nearest covered by towers", covered: true, wantX: 2160},
		{name: "ours", own: true, wantNone: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, m := testArmy(40, 40)
			a.static = aikit.NewGrid(m)
			if tc.covered {
				a.static.V[m.Sector(2000, 2100)] = 500
			}
			o := &aikit.Obs{Walls: []aikit.Wall{{X: 2000, Z: 2100}, {X: 2160, Z: 2100}, {X: 2000, Z: 1800}}}
			if tc.own {
				o.Own = append(o.Own, aikit.OwnUnit{Info: building, Built: true, X: 2100, Z: 2200})
			}
			b := &core.Board{K: &aikit.Kit{}, O: o}
			a.listEnemyWalls(o)
			s := &squad{tx: 2000, tz: 3000}
			s.present.dps = 200
			x, _, ok := a.breachPiece(b, s, 2000, 2000)
			if ok == tc.wantNone || ok && x != tc.wantX {
				t.Fatalf("piece x=%d ok=%v, want x=%d (none %v)", x, ok, tc.wantX, tc.wantNone)
			}
		})
	}
}

// An engaged squad whose members stand at an enemy wall without gaining
// ground for heldTicks is walled: the members whose weapons damage walls
// fire at a piece, those that cannot and stand in enemy fire wait behind
// it, and the economy is asked for a builder. A squad still gaining ground
// is not walled, and walls=0 leaves it alone.
func TestHeldSquadBreaksTheWall(t *testing.T) {
	rocket := &content.WeaponDef{ID: 1, DamageDefault: 100, ReloadTime: 60, AreaOfEffect: 48, Range: 400}
	emg := &content.WeaponDef{ID: 2, DamageDefault: 8, ReloadTime: 5, AreaOfEffect: 8, Range: 180}
	gun := func(i int32, w *content.WeaponDef) *aikit.UnitInfo {
		return &aikit.UnitInfo{Index: i, Role: aikit.RoleMobile | aikit.RoleCombat, DPS: 60, HP: 600, Value: 120, Range: w.Range,
			Def: &content.UnitDef{CanMove: true, BMCode: 1, Weapon1Def: w}}
	}
	launcher, small := gun(0, rocket), gun(1, emg)
	for _, tc := range []struct {
		name     string
		progress bool
		off      bool
		walled   bool
	}{
		{name: "held at the wall", walled: true},
		{name: "gaining ground", progress: true},
		{name: "walls=0", off: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, m := testArmy(40, 40)
			a.P.Walls = !tc.off
			a.static, a.danger = aikit.NewGrid(m), aikit.NewGrid(m)
			a.danger.V[m.Sector(2000, 1950)] = 100
			a.classes = []uclass{classOf(launcher, 0), classOf(small, 0)}
			a.avail = 10
			o := &aikit.Obs{
				Own: []aikit.OwnUnit{
					{H: 1, Info: launcher, Built: true, X: 1950, Z: 1950},
					{H: 2, Info: small, Built: true, X: 2000, Z: 1950},
				},
				Walls: []aikit.Wall{{X: 2000, Z: 2032}, {X: 2032, Z: 2032}},
			}
			b := &core.Board{K: &aikit.Kit{Persona: aikit.PersonaMed, Map: m}, O: o, Tick: 1000}
			s := &a.sq[sqMain]
			s.active, s.members, s.state = true, []int32{0, 1}, stEngage
			s.cx, s.cz, s.tx, s.tz = 1975, 1950, 2000, 2600
			s.present.value, s.total = 240, force{dps: 120, hp: 1200, value: 240}
			if walled, _ := a.walls(b, s); walled {
				t.Fatal("walled on first sight of the wall")
			}
			b.Tick += heldTicks(aikit.PersonaMed.Skill)
			o.WallsTick = b.Tick
			if tc.progress {
				o.Own[1].Z += breachProgress + 10
			}
			walled, _ := a.walls(b, s)
			if walled != tc.walled {
				t.Fatalf("walled %v, want %v", walled, tc.walled)
			}
			if !tc.walled {
				return
			}
			if um := a.unit(&o.Own[0]); um.ordKind != okBreach || um.ordX != s.wallX || um.ordZ != s.wallZ {
				t.Errorf("the rocket launcher carries order %d at (%d,%d), want fire at the piece (%d,%d)", um.ordKind, um.ordX, um.ordZ, s.wallX, s.wallZ)
			}
			if um := a.unit(&o.Own[1]); um.ordKind != okHold || um.ordZ >= s.wallZ {
				t.Errorf("the small-arms unit carries order %d at z %d, want to wait behind the piece", um.ordKind, um.ordZ)
			}
			if b.BreachTick != b.Tick || b.BreachX != s.wallX || b.BreachZ != s.wallZ {
				t.Errorf("breach request (%d,%d) at %d, want the piece at %d", b.BreachX, b.BreachZ, b.BreachTick, b.Tick)
			}
		})
	}
}

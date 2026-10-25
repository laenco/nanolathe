package tactics

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/aikit"
	"github.com/nanolathe-gg/nanolathe/internal/aikit/core"
)

// An enemy army closing on a base its towers cannot hold calls the main
// army home while it is still on its way out, not once the enemy stands
// among the buildings (the 2026-10-20 team replay: a Modern ally's army left
// for a small target 2,300 wu away as 40 units walked in unseen). The army
// stands a third of the way toward the force, among the towers. A force
// beyond the warning radius, one too small to matter, one the base holds,
// one the army is already fighting and one no nearer home than the army
// leave the army alone, as does warn=0.
func TestApproachCallsArmyHome(t *testing.T) {
	const home = 1000
	gun := &aikit.UnitInfo{Index: 0, DPS: 50, HP: 1000, Value: 100, Range: 300, Role: aikit.RoleMobile | aikit.RoleCombat}
	tower := &aikit.UnitInfo{Index: 1, DPS: 400, HP: 6000, Value: 400, Range: 900, Role: aikit.RoleDefense}
	for _, tc := range []struct {
		name     string
		enemyX   int32
		n        int
		blips    bool
		towers   int
		fighting bool
		mainZ    int32 // the army's distance out; 0 is 3000
		off      bool
		called   bool
	}{
		{name: "army closing in", enemyX: home + 1800, n: 10, called: true},
		{name: "radar contacts closing in", enemyX: home + 1800, n: 6, blips: true, called: true},
		{name: "beyond the warning radius", enemyX: home + warnRadius + 100, n: 10},
		{name: "a raiding pair", enemyX: home + 1800, n: 2},
		{name: "towers hold it", enemyX: home + 1800, n: 10, towers: 4},
		{name: "already fighting it", enemyX: home + 1800, n: 10, fighting: true},
		{name: "army not away", enemyX: home + 1800, n: 10, mainZ: 2300},
		{name: "warn=0", enemyX: home + 1800, n: 10, off: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, m := testArmy(60, 60)
			a.P.Warn = !tc.off
			a.classes = make([]uclass, 2)
			a.asset = aikit.NewGrid(m)
			mainZ := tc.mainZ
			if mainZ == 0 {
				mainZ = 3000
			}
			o := &aikit.Obs{Own: []aikit.OwnUnit{{Info: gun, Built: true, Tag: sqMain, X: home, Z: home + mainZ}}}
			for i := 0; i < tc.n; i++ {
				c := aikit.Contact{Owner: 2, X: tc.enemyX, Z: home + int32(i)*20, HPPct: 100, Visible: true, Built: true, Info: gun}
				if tc.blips {
					c.Info, c.Visible = nil, false
				}
				o.Enemy = append(o.Enemy, c)
			}
			b := &core.Board{K: &aikit.Kit{}, O: o, HomeX: home, HomeZ: home, Commander: -1, Tick: 900}
			for i := 0; i < tc.towers; i++ {
				o.Own = append(o.Own, aikit.OwnUnit{Info: tower, Built: true, HP: tower.HP, X: home + 600, Z: home + int32(i)*40})
				b.Defenses = append(b.Defenses, int32(len(o.Own)-1))
			}
			s := &a.sq[sqMain]
			s.active, s.members = true, []int32{0}
			s.cx, s.cz = home, home+mainZ
			s.state, s.tx, s.tz = stApproach, home, home+4000
			if tc.fighting {
				s.state, s.tx, s.tz = stEngage, tc.enemyX, home
			}
			was := s.state
			a.findApproach(b)
			a.answerApproach(b)
			if !tc.called {
				if s.state != was {
					t.Fatalf("army state %s, want it left %s", stateNames[s.state], stateNames[was])
				}
				return
			}
			if s.state != stDefend {
				t.Fatalf("army state %s, want defend", stateNames[s.state])
			}
			if wantX := int32(home + (tc.enemyX-home)/3); s.defX < wantX-60 || s.defX > wantX+60 || s.defZ < home-60 || s.defZ > home+120 {
				t.Errorf("army called to (%d,%d), want about (%d,%d)", s.defX, s.defZ, wantX, home)
			}
		})
	}
}

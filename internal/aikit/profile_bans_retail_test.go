//go:build retail

package aikit_test

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/ai"
	"github.com/nanolathe-gg/nanolathe/internal/aikit"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/headless"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/testsupport/retailcat"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// Modern AI profile bans on the retail assets (docs/DESIGN_SESSIONS_AI_SAVE.md
// "Modern AI profile bans"). Over the content's own default profile, a
// Modern player's table bans the geothermal plants, which every plan limits
// to none, and none of the stock producers its economy builds. With Arm's
// solar and wind plants limited to none in every plan of a cloned catalog's
// default profile, no build list offers them and Arm's Modern player never
// holds one in a skirmish where it otherwise builds them.
func TestModernAIHonoursProfileBansRetail(t *testing.T) {
	cat, fs := retailcat.Shared(t)
	const mapName = "Great Divide"
	if _, ok := cat.Maps[content.CanonicalKey(mapName)]; !ok {
		t.Skipf("retail map %q is absent", mapName)
	}
	for _, key := range []string{"armsolar", "armwin", "armgeo", "corgeo"} {
		if _, ok := cat.Unit(key); !ok {
			t.Skipf("retail unit %q is absent", key)
		}
	}
	// Two minutes: Arm's Modern player has built its first power plants.
	const ticks = 3600
	// play runs Arm and Core as Modern players and reports the most solar and
	// wind plants Arm held at once, records under construction included.
	play := func(cat *content.Catalog, check func(k *aikit.Kit)) int {
		t.Helper()
		capTestHost = func(m *ai.Manager) *aikit.Host { return aikit.NewHost(m, utilTac(), aikit.PersonaMed) }
		defer func() { capTestHost = nil }()
		cfg := session.SkirmishConfig{MapName: mapName, NumPlayers: 2, Location: 1}
		for i := 0; i < 2; i++ {
			cfg.Players[i] = session.SkirmishPlayer{Controller: session.SkirmishControllerComputer, AllyGroup: i, Color: i, Side: i, Metal: session.SkirmishDefaultMetal, Energy: session.SkirmishDefaultEnergy}
		}
		cfg.ApplyDefaults()
		cfg.Players[0].Side, cfg.Players[1].Side = 0, 1
		if err := cfg.ApplyComputerAI([]session.ComputerAI{{Row: session.ComputerAIEveryRow, Controller: ai.ControllerModern}}); err != nil {
			t.Fatal(err)
		}
		battle, err := headless.ComposeFreshBattle(headless.FreshBattleRequest{
			Gameplay: gameplay.Modern, Kind: headless.ScenarioDirectOTA, Map: mapName, LocalOwner: -1,
			Difficulty: 1, Skirmish: cfg, SimulationSeed: 7, CRTSeed: 7,
			FS: fs, Catalog: cat, AutomatedPlayers: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		s := battle.Session
		defer closeHosts(s)
		peak := 0
		for s.Clock.GlobalTick < ticks && s.State != session.StatePostBattle {
			s.Step(s.Clock.ScaledAnchor + 1)
			n := 0
			s.Units.ForEachPlayerSliceLive(0, func(u *units.Unit) {
				if u.Def != nil && (u.Def.CanonicalKey == "armsolar" || u.Def.CanonicalKey == "armwin") {
					n++
				}
			})
			peak = max(peak, n)
		}
		h, ok := s.AI[0].Ext.(*aikit.Host)
		if !ok || !s.ModernAIPlayer(0) {
			t.Fatal("Arm is not a Modern player")
		}
		h.Join()
		if h.Thinks == 0 {
			t.Fatal("Arm's Modern player never thought")
		}
		check(h.Kit())
		return peak
	}

	stock := play(cat, func(k *aikit.Kit) {
		for _, key := range []string{"armgeo", "corgeo"} {
			if a, by := k.Allowance(k.Table.Lookup(key)), offeredBy(k.Table, key); a != 0 || by != "" {
				t.Errorf("%s: allowance %d, offered by %q; want neither: the default profile limits it to none in every plan", key, a, by)
			}
		}
		for _, key := range []string{"armmex", "armsolar", "armwin", "armfus", "armckfus", "armuwfus", "armmakr", "armtide", "cormex", "corsolar", "corwin", "corfus", "corckfus", "coruwfus", "cormakr", "cortide"} {
			if info := k.Table.Lookup(key); info != nil && (k.Allowance(info) <= 0 || offeredBy(k.Table, key) == "") {
				t.Errorf("%s: allowance %d, offered by %q; want it open", key, k.Allowance(info), offeredBy(k.Table, key))
			}
		}
	})
	if stock == 0 {
		t.Fatalf("Arm built no solar or wind plant in %d ticks: the control shows nothing", ticks)
	}

	banned := cat.Clone()
	p := banned.AIProfiles["default"]
	if p == nil {
		t.Skip("the content has no default AI profile")
	}
	for _, d := range []string{"easy", "medium", "hard"} {
		if plan := p.Plans[d]; plan != nil && plan.Limits != nil {
			plan.Limits["armsolar"], plan.Limits["armwin"] = 0, 0
		}
	}
	if got := play(banned, func(k *aikit.Kit) {
		for _, key := range []string{"armsolar", "armwin"} {
			if a, by := k.Allowance(k.Table.Lookup(key)), offeredBy(k.Table, key); a != 0 || by != "" {
				t.Errorf("banned %s: allowance %d, offered by %q", key, a, by)
			}
		}
	}); got != 0 {
		t.Errorf("Arm held %d banned solar or wind plants, want none (%d without the ban)", got, stock)
	}
}

// offeredBy names a definition whose build list holds key, or "" when none
// does.
func offeredBy(t *aikit.Table, key string) string {
	p := t.Lookup(key)
	for _, u := range t.Units {
		if u.Buildable(p) {
			return u.Key
		}
	}
	return ""
}

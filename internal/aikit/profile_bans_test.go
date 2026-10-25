package aikit

import (
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// TestModernAIProfileBans locks the Modern AI's profile bans
// (docs/DESIGN_SESSIONS_AI_SAVE.md "Modern AI profile bans"): the table bans
// the economy producers the content's default AI profile limits to none
// under every difficulty, through each difficulty's plan or the any plan,
// and nothing one plan limits alone, nothing limited above none and no other
// role. No build list holds a banned producer, it has no allowance, and it
// is never requested, while the others keep their place.
func TestModernAIProfileBans(t *testing.T) {
	mk := func(name string, f func(*content.UnitDef)) *content.UnitDef {
		d := &content.UnitDef{DefinitionHeader: content.DefinitionHeader{CanonicalKey: name}, UnitName: strings.ToUpper(name), MaxDamage: 100, LimitEnabled: true, Limit: -1}
		f(d)
		return d
	}
	all := []string{"quark", "mex", "store", "ckfus", "fusion", "maker", "solar", "wall"}
	defs := map[string]*content.UnitDef{
		"con":    mk("con", func(d *content.UnitDef) { d.CanMove, d.BMCode, d.Builder = true, 1, true }),
		"quark":  mk("quark", func(d *content.UnitDef) { d.EnergyMake = 600 }),
		"mex":    mk("mex", func(d *content.UnitDef) { d.ExtractsMetal = 1 }),
		"store":  mk("store", func(d *content.UnitDef) { d.MetalStorage = 1000 }),
		"ckfus":  mk("ckfus", func(d *content.UnitDef) { d.EnergyMake = 1000 }),
		"fusion": mk("fusion", func(d *content.UnitDef) { d.EnergyMake = 1000 }),
		"maker":  mk("maker", func(d *content.UnitDef) { d.MakesMetal = 1 }),
		"solar":  mk("solar", func(d *content.UnitDef) { d.EnergyMake = 20 }),
		"wall":   mk("wall", func(*content.UnitDef) {}),
	}
	plan := func(limits map[string]int32) *content.AIPlan { return &content.AIPlan{Limits: limits} }
	// A page-only producer must be filtered after the installed products join
	// the build list ("Modern AI page products").
	cat := &content.Catalog{Units: defs, BuildMenus: map[string]*content.BuildMenuPage{"con": {Buttons: all[1:], InstalledProducts: []string{"quark", "solar"}}},
		AIProfiles: map[string]*content.AIProfile{"default": {Name: "default", Plans: map[string]*content.AIPlan{
			"easy":   plan(map[string]int32{"quark": 0, "ckfus": 0, "fusion": 1, "wall": 0}),
			"medium": plan(map[string]int32{"quark": 0, "fusion": 1, "wall": 0}),
			"hard":   plan(map[string]int32{"quark": 0, "fusion": 1, "wall": 0, "maker": 2}),
			"any":    plan(map[string]int32{"mex": 0, "store": 0, "maker": 0}),
		}}}}
	tab := BuildTable(cat, nil)
	con := tab.Lookup("con")
	if !tab.Lookup("store").Role.Has(RoleStorage) || tab.Lookup("wall").Role&economyRoles != 0 {
		t.Fatal("fixture roles")
	}
	banned := map[string]bool{"quark": true, "mex": true, "store": true}
	k := &Kit{Table: tab}
	for _, key := range all {
		info := tab.Lookup(key)
		if info.banned != banned[key] || con.Buildable(info) == banned[key] {
			t.Errorf("%s (role %v): banned %v, in the build list %v; want banned %v", key, info.Role, info.banned, con.Buildable(info), banned[key])
		}
		if a := k.Allowance(info); (a > 0) == banned[key] {
			t.Errorf("%s allowance %d", key, a)
		}
	}

	// A banned product is never requested; another is.
	k.out, k.ownIdx, k.inst = &batch{}, []int32{-1, 0}, []*units.Unit{nil, {}}
	builder := []pool.Handle{1}
	k.push(Command{Kind: CmdBuild, Product: tab.Lookup("quark")}, builder)
	k.push(Command{Kind: CmdBuild, Product: tab.Lookup("solar")}, builder)
	if len(k.out.cmds) != 1 || k.out.cmds[0].Product != tab.Lookup("solar") {
		t.Fatalf("requests %+v, want only the solar plant", k.out.cmds)
	}

	// Content without a default profile bans nothing.
	cat.AIProfiles = nil
	if tab := BuildTable(cat, nil); len(tab.Lookup("con").Builds) != len(all) || tab.Lookup("quark").banned {
		t.Fatal("content without a default profile banned something")
	}
}

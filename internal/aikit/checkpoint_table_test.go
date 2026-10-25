package aikit

import (
	"maps"
	"slices"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/ai"
	"github.com/nanolathe-gg/nanolathe/internal/construction"
	"github.com/nanolathe-gg/nanolathe/internal/content"
)

func checkpointTableCatalog(t *testing.T) (*content.Catalog, *content.CheckpointKeys) {
	t.Helper()
	unit := func(key string) *content.UnitDef {
		return &content.UnitDef{DefinitionHeader: content.DefinitionHeader{CanonicalKey: key}, UnitName: key, ObjectName: "fixture", Side: "authored", MaxDamage: 120, FootprintX: 2, FootprintZ: 3}
	}
	commander, fighter, tank, wall, wall2 := unit("commander"), unit("fighter"), unit("tank"), unit("wall"), unit("wall2")
	commander.Commander, commander.Builder, commander.CanMove, commander.BMCode = true, true, true, 1
	commander.WorkerTime, commander.BuildDistance = 75, 90
	fighter.CanFly, fighter.CanMove, fighter.BMCode = true, true, 1
	fighter.LimitEnabled, fighter.Limit = true, 4
	tank.CanMove, tank.BMCode, tank.LimitEnabled, tank.Limit = true, 1, true, 7
	weapon := &content.WeaponDef{DefinitionHeader: content.DefinitionHeader{CanonicalKey: "gun"}, ID: 1, DamageDefault: 20, Damage: map[string]int32{"fighter": 60}, ReloadTime: 30, Range: 240}
	fighter.Weapon1Def, tank.Weapon1Def = weapon, weapon
	wall.IsFeature, wall.Corpse, wall2.IsFeature, wall2.Corpse = true, "barrier", true, "barrier"
	feature := &content.FeatureDef{DefinitionHeader: content.DefinitionHeader{CanonicalKey: "barrier"}, Blocking: true}
	cat := &content.Catalog{
		Units:    map[string]*content.UnitDef{"commander": commander, "fighter": fighter, "tank": tank, "wall": wall, "wall2": wall2},
		Weapons:  map[string]*content.WeaponDef{"gun": weapon},
		Features: map[string]*content.FeatureDef{"barrier": feature},
		BuildMenus: map[string]*content.BuildMenuPage{"commander": {
			DefinitionHeader: content.DefinitionHeader{CanonicalKey: "commander"}, Builder: "commander",
			Buttons: []string{"tank", "wall", "tank"}, AuthoredButtons: []string{"tank", "wall", "tank", "fighter"},
		}},
	}
	return cat, checkpointCommandKeys(t, cat)
}

func TestCheckpointTableRetainsFinalConstructionAndOriginalRule(t *testing.T) {
	cat, keys := checkpointTableCatalog(t)
	for _, tc := range []struct {
		name     string
		rules    construction.Rules
		kind     uint8
		products []string
	}{
		{"nil", nil, 0, []string{"tank", "wall", "tank"}},
		{"strict", construction.StrictRules{}, 1, []string{"tank", "wall", "tank"}},
		{"strict pointer", &construction.StrictRules{}, 1, []string{"tank", "wall", "tank"}},
		{"community", construction.CommunityRules{}, 2, []string{"tank", "wall", "tank"}},
		{"community pointer", &construction.CommunityRules{}, 2, []string{"tank", "wall", "tank"}},
		{"modern", &construction.ModernRules{}, 3, []string{"tank", "wall", "tank", "fighter"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := &ai.Manager{ConstructionRules: tc.rules}
			table := BuildTable(cat, manager.ConstructionRules)
			// A mode switch retains this table. Validation must report its
			// original rule, not reclassify its products under the new one.
			manager.ConstructionRules = &construction.ModernRules{}
			if tc.kind == 3 {
				manager.ConstructionRules = construction.StrictRules{}
			}
			if kind, err := table.ValidateCheckpointBindings(cat, keys); err != nil || kind != tc.kind {
				t.Fatalf("original kind = %d, %v; want %d", kind, err, tc.kind)
			}
			var products []string
			for _, u := range table.Units[0].Builds {
				products = append(products, u.Key)
			}
			if !slices.Equal(products, tc.products) {
				t.Fatalf("products = %v; want %v", products, tc.products)
			}
			fighter := table.byKey["fighter"]
			if !fighter.Role.Has(RoleFighter) || fighter.Role.Has(RoleBomber) || fighter.AirDPS != 60 {
				t.Fatalf("final fighter classification missing: %+v", fighter)
			}
			if table.byKey["tank"].Depth != 1 || table.Units[0].Depth != 0 ||
				!slices.Equal(table.Capped, []*UnitInfo{fighter, table.byKey["tank"]}) || fighter.capSlot != 1 || table.Capped[1].capSlot != 2 {
				t.Fatal("snapshot hook changed depth or cap construction")
			}
			if len(table.checkpoint.defensiveFeatures.rows) != 1 || table.byKey["wall"].FinishedFeature != table.byKey["wall2"].FinishedFeature {
				t.Fatal("shared finished feature was not deduplicated")
			}
		})
	}
}

func TestCheckpointTableDetachedCollectionsRefuseMutation(t *testing.T) {
	cat, keys := checkpointTableCatalog(t)
	for _, tc := range []struct {
		name   string
		mutate func(*Table)
	}{
		{"unit order", func(s *Table) { s.Units[0], s.Units[1] = s.Units[1], s.Units[0] }},
		{"unit removed", func(s *Table) { s.Units = s.Units[:len(s.Units)-1] }},
		{"equal unit copy", func(s *Table) { u := *s.Units[0]; s.Units[0] = &u }},
		{"nil unit", func(s *Table) { s.Units[0] = nil }},
		{"cap order", func(s *Table) { s.Capped[0], s.Capped[1] = s.Capped[1], s.Capped[0] }},
		{"cap removed", func(s *Table) { s.Capped = s.Capped[:1] }},
		{"cap slot", func(s *Table) { s.Capped[0].capSlot++ }},
		{"build order", func(s *Table) { b := s.Units[0].Builds; b[0], b[1] = b[1], b[0] }},
		{"build duplicate lost", func(s *Table) { s.Units[0].Builds = s.Units[0].Builds[:2] }},
		{"equal build copy", func(s *Table) { u := *s.Units[0].Builds[0]; s.Units[0].Builds[0] = &u }},
		{"key missing", func(s *Table) { delete(s.byKey, "tank") }},
		{"key replaced same length", func(s *Table) { u := s.byKey["tank"]; delete(s.byKey, "tank"); s.byKey["Tank"] = u }},
		{"key value replaced", func(s *Table) { s.byKey["tank"] = s.byKey["fighter"] }},
		{"key extra alias", func(s *Table) { s.byKey["alias"] = s.Units[0] }},
		{"definition missing", func(s *Table) { delete(s.byDef, s.Units[0].Def) }},
		{"definition replaced same length", func(s *Table) { u := s.Units[0]; d := *u.Def; delete(s.byDef, u.Def); s.byDef[&d] = u }},
		{"definition value replaced", func(s *Table) { s.byDef[s.Units[0].Def] = s.Units[1] }},
		{"feature missing", func(s *Table) { delete(s.defensiveFeatures, s.byKey["wall"].FinishedFeature) }},
		{"feature false", func(s *Table) { s.defensiveFeatures[s.byKey["wall"].FinishedFeature] = false }},
		{"feature replaced same length", func(s *Table) {
			f := s.byKey["wall"].FinishedFeature
			copy := *f
			delete(s.defensiveFeatures, f)
			s.defensiveFeatures[&copy] = true
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			table := BuildTable(cat, nil)
			tc.mutate(table)
			if _, err := table.ValidateCheckpointBindings(cat, keys); err == nil {
				t.Fatal("mutated table admitted")
			}
		})
	}
	// Collection allocation identities are not gameplay identities. Equal
	// detached containers remain admitted when their members and shape match.
	table := BuildTable(cat, nil)
	table.Units, table.Capped = slices.Clone(table.Units), slices.Clone(table.Capped)
	table.byKey, table.byDef, table.defensiveFeatures = maps.Clone(table.byKey), maps.Clone(table.byDef), maps.Clone(table.defensiveFeatures)
	for _, u := range table.Units {
		u.Builds = slices.Clone(u.Builds)
	}
	if _, err := table.ValidateCheckpointBindings(cat, keys); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointTableDerivedValuesAndIdentityAreSealed(t *testing.T) {
	cat, keys := checkpointTableCatalog(t)
	for _, tc := range []struct {
		name   string
		mutate func(*UnitInfo)
	}{
		{"row identity", func(u *UnitInfo) { u.Index++ }},
		{"canonical name", func(u *UnitInfo) { u.Key = "Commander" }},
		{"side", func(u *UnitInfo) { u.Side = "other" }},
		{"classification", func(u *UnitInfo) { u.Role ^= RoleBuilder }},
		{"price", func(u *UnitInfo) { u.Value++ }},
		{"resource yield", func(u *UnitInfo) { u.EnergyMake++ }},
		{"weapon estimate", func(u *UnitInfo) { u.AirDPS++ }},
		{"travel estimate", func(u *UnitInfo) { u.Speed++ }},
		{"build reach", func(u *UnitInfo) { u.BuildRange++ }},
		{"footprint", func(u *UnitInfo) { u.FootZ++ }},
		{"build depth", func(u *UnitInfo) { u.Depth++ }},
		{"restriction", func(u *UnitInfo) { u.Cap++ }},
		{"profile ban", func(u *UnitInfo) { u.banned = !u.banned }},
		{"equal definition copy", func(u *UnitInfo) { d := *u.Def; u.Def = &d }},
		{"finished feature", func(u *UnitInfo) { u.FinishedFeature = cat.Features["barrier"] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			table := BuildTable(cat, nil)
			tc.mutate(table.Units[0])
			if _, err := table.ValidateCheckpointBindings(cat, keys); err == nil {
				t.Fatal("mutated derived value admitted")
			}
		})
	}
	table := BuildTable(cat, nil)
	copy := *table
	if _, err := copy.ValidateCheckpointBindings(cat, keys); err == nil {
		t.Fatal("copied table admitted")
	}
	catCopy := *cat
	if _, err := table.ValidateCheckpointBindings(&catCopy, keys); err == nil {
		t.Fatal("copied catalog admitted")
	}
	other, foreignKeys := checkpointTableCatalog(t)
	if _, err := table.ValidateCheckpointBindings(other, keys); err == nil {
		t.Fatal("foreign catalog admitted")
	}
	if _, err := table.ValidateCheckpointBindings(cat, foreignKeys); err == nil {
		t.Fatal("foreign admitted identities accepted")
	}
	if _, err := table.ValidateCheckpointBindings(cat, nil); err == nil {
		t.Fatal("absent keys admitted")
	}
	if _, err := (*Table)(nil).ValidateCheckpointBindings(cat, keys); err == nil {
		t.Fatal("absent table admitted")
	}
	if _, err := (&Table{}).ValidateCheckpointBindings(cat, keys); err == nil {
		t.Fatal("unconstructed table admitted")
	}
	// A feature can be foreign even when every UnitDef has admitted identity.
	cat.Features["barrier"] = &content.FeatureDef{Blocking: true}
	if _, err := BuildTable(cat, nil).ValidateCheckpointBindings(cat, keys); err == nil {
		t.Fatal("unadmitted feature accepted")
	}
}

func TestCheckpointTableNilAndEmptyShape(t *testing.T) {
	cat := &content.Catalog{}
	keys := checkpointCommandKeys(t, cat)
	for _, mutate := range []func(*Table){
		func(s *Table) { s.Units = []*UnitInfo{} },
		func(s *Table) { s.Capped = []*UnitInfo{} },
		func(s *Table) { s.byKey = nil },
		func(s *Table) { s.byDef = nil },
		func(s *Table) { s.defensiveFeatures = nil },
	} {
		table := BuildTable(cat, nil)
		if _, err := table.ValidateCheckpointBindings(cat, keys); err != nil {
			t.Fatal(err)
		}
		mutate(table)
		if _, err := table.ValidateCheckpointBindings(cat, keys); err == nil {
			t.Fatal("changed nil/empty shape admitted")
		}
	}
	table := BuildTable(nil, nil)
	if table.checkpoint == nil {
		t.Fatal("nil-catalog constructor did not snapshot")
	}
	if _, err := table.ValidateCheckpointBindings(nil, keys); err == nil {
		t.Fatal("nil catalog admitted")
	}
	cat, keys = checkpointTableCatalog(t)
	table = BuildTable(cat, nil)
	table.byKey["tank"].Builds = []*UnitInfo{}
	if _, err := table.ValidateCheckpointBindings(cat, keys); err == nil {
		t.Fatal("nil build edges changed to empty")
	}
}

// The slice makes this implementation noncomparable; its method also detects
// accidental reclassification during capture. Only ordinary BuildTable may
// borrow its products, exactly as before provenance metadata was added.
type checkpointTableCustomRules struct {
	construction.StrictRules
	products []string
	calls    *int
	forbid   *bool
}

func (r checkpointTableCustomRules) BuildProducts(*content.BuildMenuPage) []string {
	if *r.forbid {
		panic("checkpoint invoked construction rules")
	}
	*r.calls++
	return r.products
}

func TestCheckpointTableUnsupportedRulesKeepOrdinaryBehavior(t *testing.T) {
	cat, keys := checkpointTableCatalog(t)
	calls, forbid := 0, false
	rules := checkpointTableCustomRules{products: []string{"wall", "tank", "wall"}, calls: &calls, forbid: &forbid}
	table := BuildTable(cat, rules)
	if calls != 1 || !slices.Equal(table.Units[0].Builds, []*UnitInfo{table.byKey["wall"], table.byKey["tank"], table.byKey["wall"]}) {
		t.Fatalf("ordinary custom construction changed: calls=%d", calls)
	}
	forbid = true
	if _, err := table.ValidateCheckpointBindings(cat, keys); err == nil {
		t.Fatal("custom rule admitted")
	}
	if calls != 1 {
		t.Fatal("capture called custom rules")
	}
	// Modern's ordinary BuildProducts does not dereference its receiver;
	// metadata must still refuse the typed-nil installation, without changing
	// that preexisting ordinary construction behavior.
	table = BuildTable(cat, (*construction.ModernRules)(nil))
	if len(table.Units[0].Builds) != 4 {
		t.Fatal("typed-nil ordinary products changed")
	}
	if _, err := table.ValidateCheckpointBindings(cat, keys); err == nil {
		t.Fatal("typed-nil rules admitted")
	}
}

func TestCheckpointTableValidationIsPureAndAllocationFree(t *testing.T) {
	cat, keys := checkpointTableCatalog(t)
	table := BuildTable(cat, &construction.ModernRules{})
	snapshot := table.checkpoint
	if allocations := testing.AllocsPerRun(100, func() {
		if kind, err := table.ValidateCheckpointBindings(cat, keys); err != nil || kind != 3 {
			t.Fatalf("validate: %d, %v", kind, err)
		}
	}); allocations != 0 {
		t.Fatalf("validation allocations = %g", allocations)
	}
	// Invalid reads must not repair either the retained value or the snapshot.
	original := table.Units[0].Builds[0]
	table.Units[0].Builds[0] = nil
	for range 2 {
		if _, err := table.ValidateCheckpointBindings(cat, keys); err == nil {
			t.Fatal("invalid repeated capture admitted")
		}
	}
	if table.Units[0].Builds[0] != nil || table.checkpoint != snapshot || snapshot.values[0].Builds[0] != original {
		t.Fatal("validation mutated runtime or snapshot storage")
	}
	table.Units[0].Builds[0] = original
	if _, err := table.ValidateCheckpointBindings(cat, keys); err != nil {
		t.Fatal(err)
	}
}

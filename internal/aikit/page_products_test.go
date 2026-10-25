package aikit

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/construction"
	"github.com/nanolathe-gg/nanolathe/internal/content"
)

// A product the builder's own pages install is a candidate whatever the
// rule's membership says (Switeck's bugfix pages offer CORE's radar tower to
// a construction vehicle whose CANBUILD omits it), after that membership and
// once; a page name that is no unit adds nothing.
func TestTableAddsPageInstalledProducts(t *testing.T) {
	unit := func(key string) *content.UnitDef {
		return &content.UnitDef{DefinitionHeader: content.DefinitionHeader{CanonicalKey: key}, UnitName: key, Side: "CORE", MaxDamage: 100, FootprintX: 2, FootprintZ: 2}
	}
	cv, solar, rad := unit("corcv"), unit("corsolar"), unit("corrad")
	cv.Builder, cv.CanMove, cv.BMCode = true, true, 1
	cat := &content.Catalog{
		Units: map[string]*content.UnitDef{"corcv": cv, "corsolar": solar, "corrad": rad},
		BuildMenus: map[string]*content.BuildMenuPage{"corcv": {
			DefinitionHeader:  content.DefinitionHeader{CanonicalKey: "corcv"},
			Builder:           "corcv",
			Buttons:           []string{"corsolar"},
			InstalledProducts: []string{"CORSOLAR", "CORRAD", "CORNONE"},
		}},
	}
	for _, rules := range []construction.Rules{nil, construction.StrictRules{}, &construction.ModernRules{}} {
		builds := BuildTable(cat, rules).Lookup("corcv").Builds
		if len(builds) != 2 || builds[0].Key != "corsolar" || builds[1].Key != "corrad" {
			var keys []string
			for _, p := range builds {
				keys = append(keys, p.Key)
			}
			t.Fatalf("rules %T: builds %v, want [corsolar corrad]", rules, keys)
		}
	}
}

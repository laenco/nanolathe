package content

import (
	"slices"
	"strconv"
	"testing"
)

// A builder's numbered pages install the button gadgets that carry the
// product-slot attribute and name a definition, whatever its CANBUILD says
// [07 §9]: Switeck's bugfix pages put CORE's radar tower on a construction
// vehicle whose list omits it. A slot a download record patches is the
// record's product, not the authored name; page 0, a page after the first
// missing one, an order button, a unit name without the attribute, a gadget
// that is not a button and a name that resolves to nothing install nothing.
// Authored fixtures, no retail bytes.
func TestInstalledProductsReadAuthoredPages(t *testing.T) {
	gadget := func(id int, name string, attrs int) string {
		return "[GADGET] { [COMMON] { id=" + strconv.Itoa(id) + "; name=" + name + "; commonattribs=" + strconv.Itoa(attrs) + "; } }\n"
	}
	header := gadget(0, "HEADER", 0)
	fs := newFixtureFS(t,
		fixtureFile{path: "guis/corcv0.gui", data: header + gadget(1, "CORZERO", 4)},
		fixtureFile{path: "guis/corcv1.gui", data: header +
			gadget(1, "CORSOLAR", 4) + gadget(1, "CORRAD", 4) + gadget(1, "CORMOVE", 0) +
			gadget(1, "CORNONE", 4) + gadget(1, "CORLLT", 0) + gadget(5, "CORGEO", 4)},
		fixtureFile{path: "guis/corcv2.gui", data: header +
			gadget(1, "CORPREV", 0) + gadget(1, "CORNEXT", 0) + gadget(1, "CORBUTT", 0) +
			gadget(1, "CORGEO", 4) + gadget(1, "CORHLT", 4)},
		fixtureFile{path: "guis/corcv4.gui", data: header + gadget(1, "CORDOOM", 4)},
	)
	units := map[string]*UnitDef{}
	for _, name := range []string{"CORCV", "CORSOLAR", "CORRAD", "CORLLT", "CORGEO", "CORHLT", "CORDOOM", "CORZERO", "CORMEX"} {
		units[CanonicalKey(name)] = testUnit(name, name == "CORCV", 0)
	}
	menus := map[string]*BuildMenuPage{"corcv": {
		DefinitionHeader: DefinitionHeader{CanonicalKey: "corcv"},
		Builder:          "CORCV",
		Buttons:          []string{"CORSOLAR", "CORLLT"},
	}}
	// MENU 3 is visible page 2; BUTTON 0 is the page's fifth gadget, CORGEO.
	placements := []DownloadMenuPlacement{{Builder: "CORCV", Menu: 3, Button: 0, Product: "CORMEX", BuilderResolved: true, ProductResolved: true}}
	fillInstalledProducts(fs, units, menus, placements)
	if got, want := menus["corcv"].InstalledProducts, []string{"CORSOLAR", "CORRAD", "CORHLT"}; !slices.Equal(got, want) {
		t.Fatalf("installed products %v, want %v", got, want)
	}
}

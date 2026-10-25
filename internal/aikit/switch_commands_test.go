package aikit

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/ai"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// The Kit's switch and transport commands are one action each, name the
// state order or order code the side panel and a human's clicks use, bind a
// Load to the observed cargo, and are applied only to actors whose
// definition the switch means something to (docs/DESIGN_SESSIONS_AI_SAVE.md
// "Modern AI commands").
func TestKitSwitchAndTransportVocabulary(t *testing.T) {
	var b batch
	cargo := &units.Unit{Handle: 3}
	k := &Kit{out: &b, ownIdx: []int32{-1, 0, 1, -1}, inst: []*units.Unit{nil, {Handle: 1}, {Handle: 2}, cargo}}
	k.Load(1, 3, false)
	k.Unload(1, 640, 480, true)
	k.Activate([]pool.Handle{1, 2}, true)
	k.Activate([]pool.Handle{2}, false)
	k.Cloak([]pool.Handle{1, 2, 3}, true) // 3 is not ours: dropped from the group
	k.Cloak([]pool.Handle{2}, false)
	want := []CmdKind{CmdLoad, CmdUnload, CmdActivate, CmdDeactivate, CmdCloak, CmdDecloak}
	if len(b.cmds) != len(want) {
		t.Fatalf("%d commands, want %d", len(b.cmds), len(want))
	}
	for i, c := range b.cmds {
		if c.Kind != want[i] {
			t.Errorf("command %d kind %d, want %d", i, c.Kind, want[i])
		}
	}
	if c := b.cmds[0]; c.Target != 3 || c.target != cargo || c.count != 1 || orderCodes[c.Kind] != 6 {
		t.Errorf("load %+v: want the pickup code on the observed cargo", c)
	}
	if c := b.cmds[1]; c.X != 640 || c.Z != 480 || !c.Queued || orderCodes[c.Kind] != 5 {
		t.Errorf("unload %+v: want the unload code at the point, queued", c)
	}
	if c := b.cmds[4]; c.count != 2 {
		t.Errorf("cloak group of %d actors, want our two", c.count)
	}
	for kind, name := range map[CmdKind]string{CmdActivate: "Activate", CmdDeactivate: "Deactivate", CmdCloak: "Cloak_On", CmdDecloak: "Cloak_Off"} {
		if stateOrders[kind] != name || orders.Lookup(name) == 0 {
			t.Errorf("command %d pushes %q, want the %s state order", kind, stateOrders[kind], name)
		}
	}
	onoff := &content.UnitDef{OnOffable: true}
	cloaker := &content.UnitDef{CloakCost: 200}
	if !switchApplies(CmdActivate, onoff) || switchApplies(CmdActivate, cloaker) || !switchApplies(CmdCloak, cloaker) || switchApplies(CmdDecloak, onoff) {
		t.Error("a switch applied to a definition it means nothing to, or not to one it does")
	}
}

// The checkpoint codec's Modern vocabulary (internal/ai, which cannot import
// this package) must take every command kind the executor applies, or the
// first command of a new kind fails a checkpointed battle's history.
func TestModernIntentKindsCoverTheVocabulary(t *testing.T) {
	if int(CmdBreach) != ai.ModernIntentKinds {
		t.Fatalf("the last command kind is %d, the checkpoint vocabulary ends at %d", CmdBreach, ai.ModernIntentKinds)
	}
}

package aikit

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

func TestFailResolveBreakdown(t *testing.T) {
	var s ApplyStats
	s.noteResolve(CmdMove, &units.Unit{Def: &content.UnitDef{Commander: true}})
	s.noteResolve(CmdAttack, &units.Unit{Def: &content.UnitDef{Builder: true}})
	s.noteResolve(CmdGuard, &units.Unit{Def: &content.UnitDef{}})
	s.noteResolve(CmdAttack, nil)
	if s.FailKinds[CmdMove-1] != 1 || s.FailKinds[CmdAttack-1] != 2 || s.FailKinds[CmdGuard-1] != 1 {
		t.Fatalf("fail kinds = %v, want move 1, attack 2, guard 1", s.FailKinds)
	}
	if s.FailActors[failActorCommander] != 1 || s.FailActors[failActorBuilder] != 1 || s.FailActors[failActorCombat] != 2 {
		t.Fatalf("fail actors = %v, want commander 1, builder 1, combat 2", s.FailActors)
	}
}

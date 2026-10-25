// Build-animation functions for the Builder speed mutator
// (docs/DESIGN_MODS_MUTATORS.md "Builder speed"). This is Nanolathe policy,
// not a retail mechanism: retail runs every script function on the one fixed
// 30-tick conversion [04 §4.6]. The mutator speeds up only the functions a
// builder runs to deploy and stow its nanolathe, which are recognised from
// the program's own call graph.

package cob

import "sort"

// engineCallbackNames are the forty script names the engine starts by name,
// and the only ones [04 R-CB-01 §1]. Names are case-sensitive; setSFXoccupy
// keeps its lower-case s.
var engineCallbackNames = [...]string{
	"Activate", "AimFromPrimary", "AimFromSecondary", "AimFromTertiary",
	"AimPrimary", "AimSecondary", "AimTertiary", "BeginTransport", "Create",
	"Deactivate", "EndTransport", "FirePrimary", "FireSecondary",
	"FireTertiary", "HitByWeapon", "Killed", "MoveRate1", "MoveRate2",
	"MoveRate3", "QueryBuildInfo", "QueryLandingPad", "QueryNanoPiece",
	"QueryPrimary", "QuerySecondary", "QueryTertiary", "QueryTransport",
	"RockUnit", "SetDirection", "SetMaxReloadTime", "SetSpeed", "SweetSpot",
	"StartBuilding", "StartMoving", "StopBuilding", "StopMoving",
	"TakeDamage", "TargetCleared", "TransportDrop", "TransportPickup",
	"setSFXoccupy",
}

// isBuildAnimationRoot reports whether an engine callback name is one of the
// four whose reach defines the build-animation set: the build edge pair and
// the activation edge pair [04 R-CB-01 §2].
func isBuildAnimationRoot(name string) bool {
	switch name {
	case "StartBuilding", "StopBuilding", "Activate", "Deactivate":
		return true
	}
	return false
}

// instructionWords is each opcode's length in code words, operands included,
// keyed by the dispatched opcode [04 §4.3][fmt cob]. It is the same decoding
// runThread performs; an opcode absent here is one the VM does not execute.
var instructionWords = map[uint32]int{
	0x10001000: 3, 0x10002000: 3, 0x10003000: 3, 0x10004000: 3, 0x10005000: 2, 0x10006000: 2,
	0x10007000: 2, 0x10008000: 2, 0x10009000: 2, 0x1000a000: 2, 0x1000b000: 3, 0x1000c000: 3,
	0x1000d000: 2, 0x1000e000: 2, 0x1000f000: 2, 0x10011000: 3, 0x10012000: 3, 0x10013000: 1,
	0x10021000: 2, 0x10022000: 1, 0x10023000: 2, 0x10024000: 1, 0x10031000: 1, 0x10032000: 1,
	0x10033000: 1, 0x10034000: 1, 0x10035000: 1, 0x10036000: 1, 0x10037000: 1, 0x10038000: 1,
	0x10041000: 1, 0x10042000: 1, 0x10043000: 1, 0x10044000: 1, 0x10045000: 1, 0x10051000: 1,
	0x10052000: 1, 0x10053000: 1, 0x10054000: 1, 0x10055000: 1, 0x10056000: 1, 0x10057000: 1,
	0x10058000: 1, 0x10059000: 1, 0x1005a000: 1, 0x10061000: 3, 0x10062000: 3, 0x10063000: 3,
	0x10064000: 2, 0x10065000: 1, 0x10066000: 2, 0x10067000: 1, 0x10068000: 1, 0x10071000: 2,
	0x10082000: 1, 0x10083000: 1, 0x10084000: 1,
}

// codeRange is one function's half-open range of code words.
type codeRange struct{ start, end int }

// scriptFunctions splits a program's code into its functions: one per
// distinct entry-point code index, each running to the next greater entry
// point or the end of the code. A COB function is compiled as one contiguous
// block, so the function an instruction executes in is the one whose range
// holds its word index.
func scriptFunctions(p *Program) []codeRange {
	if p == nil || len(p.Code) == 0 {
		return nil
	}
	starts := make([]int, 0, len(p.ScriptsByID))
	for _, start := range p.ScriptsByID {
		if start >= 0 && start < len(p.Code) {
			starts = append(starts, start)
		}
	}
	sort.Ints(starts)
	out := make([]codeRange, 0, len(starts))
	for i, start := range starts {
		if i > 0 && start == starts[i-1] {
			continue
		}
		out = append(out, codeRange{start: start})
	}
	for i := range out {
		if i+1 < len(out) {
			out[i].end = out[i+1].start
		} else {
			out[i].end = len(p.Code)
		}
	}
	return out
}

// functionAt is the index in fns of the function holding word pc, or -1.
func functionAt(fns []codeRange, pc int) int {
	i := sort.Search(len(fns), func(i int) bool { return fns[i].end > pc })
	if i < len(fns) && fns[i].start <= pc {
		return i
	}
	return -1
}

// callEdges lists, per function, the functions its call-script and
// start-script instructions name, decoded linearly from the function's entry
// [04 §4.3]. A word the VM would not dispatch advances one word, as a scan
// that cannot know the length must; the VM kills a thread there anyway.
func callEdges(p *Program, fns []codeRange) [][]int {
	edges := make([][]int, len(fns))
	for f, fn := range fns {
		for pc := fn.start; pc < fn.end; {
			op := p.Code[pc] & dispatchMask
			n, ok := instructionWords[op]
			if !ok {
				pc++
				continue
			}
			if (op == 0x10061000 || op == 0x10062000) && pc+1 < len(p.Code) {
				if target := codeIndexForID(p, int(p.Code[pc+1])); target >= 0 {
					if g := functionAt(fns, target); g >= 0 && fns[g].start == target {
						edges[f] = append(edges[f], g)
					}
				}
			}
			pc += n
		}
	}
	return edges
}

// reachable marks every function reachable from the named roots through the
// call graph, the roots included. A root the program does not author is
// skipped.
func reachable(p *Program, fns []codeRange, edges [][]int, roots []string) []bool {
	seen := make([]bool, len(fns))
	var stack []int
	for _, name := range roots {
		start, ok := p.Scripts[name]
		if !ok {
			continue
		}
		if f := functionAt(fns, start); f >= 0 && fns[f].start == start && !seen[f] {
			seen[f] = true
			stack = append(stack, f)
		}
	}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, g := range edges[f] {
			if !seen[g] {
				seen[g] = true
				stack = append(stack, g)
			}
		}
	}
	return seen
}

// buildAnimationRanges computes a program's build-animation functions: those
// reachable through call-script and start-script edges from StartBuilding,
// StopBuilding, Activate and Deactivate, minus every function also reachable
// from any other engine callback name [04 R-CB-01 §1]. A function a weapon,
// damage, death, movement or creation callback can reach keeps retail timing,
// because speeding it up would change more than the builder's deployment —
// a shared loop started from Create, for instance. The result is in ascending
// code order and deterministic in the bytecode alone.
func buildAnimationRanges(p *Program) []codeRange {
	fns := scriptFunctions(p)
	if len(fns) == 0 {
		return nil
	}
	var buildRoots, otherRoots []string
	for _, name := range engineCallbackNames {
		if isBuildAnimationRoot(name) {
			buildRoots = append(buildRoots, name)
		} else {
			otherRoots = append(otherRoots, name)
		}
	}
	edges := callEdges(p, fns)
	build := reachable(p, fns, edges, buildRoots)
	other := reachable(p, fns, edges, otherRoots)
	var out []codeRange
	for f, fn := range fns {
		if build[f] && !other[f] {
			out = append(out, fn)
		}
	}
	return out
}

// inBuildAnimation reports whether code word pc lies in one of the program's
// build-animation functions. Load computes the ranges once per loaded
// program; a program assembled any other way has none.
func (p *Program) inBuildAnimation(pc int) bool {
	if p == nil {
		return false
	}
	ranges := p.buildAnimation
	i := sort.Search(len(ranges), func(i int) bool { return ranges[i].end > pc })
	return i < len(ranges) && ranges[i].start <= pc
}

// SetBuilderSpeed latches the Builder speed factor k for this VM's build
// animations (docs/DESIGN_MODS_MUTATORS.md "Builder speed"). The binding sets
// it before Create from the unit definition, and it stays for the VM's life.
// Inside a build-animation function a turn, move or spin speed and a spin
// acceleration become k·value/30 per tick, and a sleep dur·30/(1000·k) ticks,
// truncated toward zero (I3). k of 0 or 1 is the identity, which leaves every
// instruction on retail's arithmetic [04 §4.6].
func (v *VM) SetBuilderSpeed(k int32) {
	if v == nil {
		return
	}
	if k < 1 {
		k = 1
	}
	v.buildRate = k
}

// animRate is the factor the instruction at pc runs under: the latched
// Builder speed inside a build-animation function, 1 everywhere else.
func (v *VM) animRate(pc int) int64 {
	if v.buildRate > 1 && v.prog.inBuildAnimation(pc) {
		return int64(v.buildRate)
	}
	return 1
}

package cob

import (
	"bytes"
	"reflect"
	"slices"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/testsupport"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

// buildAnimationFunctions names the program's build-animation functions in
// ascending code order, each by every entry-point name the script table
// gives its code index; the VM reads the ranges Load stored.
func buildAnimationFunctions(p *Program) []string {
	if p == nil {
		return nil
	}
	var out []string
	for _, fn := range buildAnimationRanges(p) {
		for id, start := range p.ScriptsByID {
			if start == fn.start && id < len(p.scriptNames) {
				out = append(out, p.scriptNames[id])
			}
		}
	}
	return out
}

// builderSpeedProgram is an authored script shaped for the Builder speed
// contract (docs/DESIGN_MODS_MUTATORS.md "Builder speed"). StartBuilding
// starts RequestState, which turns, moves, sleeps and then calls Shared;
// Shared is also called by Create and by AimPrimary, so it is reachable from
// another engine entry point and keeps retail timing. Deactivate spins and
// Activate stops a spin, the two other build roots.
func builderSpeedProgram(t *testing.T) *Program {
	t.Helper()
	code := []uint32{
		// StartBuilding @0
		0x10061000, 3, 0, // start-script RequestState
		0x10021001, 0, 0x10065000,
		// Create @6
		0x10062000, 4, 0, // call-script Shared
		0x10021001, 0, 0x10065000,
		// AimPrimary @12
		0x10062000, 4, 0, // call-script Shared
		0x10021001, 0, 0x10065000,
		// RequestState @18
		0x10021001, 300, 0x10021001, 10000, 0x10002000, 0, 1, // turn piece 0 axis 1, speed 300
		0x10021001, 90, 0x10021001, 1000, 0x10001000, 0, 0, // move piece 0 axis 0, speed 90
		0x10021001, 1000, 0x10013000, // sleep 1000 ms
		0x10062000, 4, 0, // call-script Shared
		0x10021001, 0, 0x10065000,
		// Shared @41
		0x10021001, 300, 0x10021001, 20000, 0x10002000, 0, 2, // turn piece 0 axis 2, speed 300
		0x10021001, 1000, 0x10013000, // sleep 1000 ms
		0x10021001, 0, 0x10065000,
		// Deactivate @54
		0x10021001, 60, 0x10021001, 600, 0x10003000, 0, 0, // spin piece 0 axis 0, speed 600 accelerate 60
		0x10021001, 0, 0x10065000,
		// Activate @64
		0x10021001, 150, 0x10004000, 0, 0, // stop-spin piece 0 axis 0, decelerate 150
		0x10021001, 0, 0x10065000,
	}
	names := []string{"StartBuilding", "Create", "AimPrimary", "RequestState", "Shared", "Deactivate", "Activate"}
	prog, err := Load(makeCOB(code, names, []uint32{0, 6, 12, 18, 41, 54, 64}, []string{"base"}))
	if err != nil {
		t.Fatal(err)
	}
	return prog
}

// TestBuildAnimationSetExcludesSharedCode: the set is the build roots'
// call graph minus everything another engine entry point reaches. Shared is
// reached from StartBuilding through RequestState, but also from Create and
// from the AimPrimary weapon callback, so it is excluded.
func TestBuildAnimationSetExcludesSharedCode(t *testing.T) {
	prog := builderSpeedProgram(t)
	want := []string{"StartBuilding", "RequestState", "Deactivate", "Activate"}
	if got := buildAnimationFunctions(prog); !reflect.DeepEqual(got, want) {
		t.Fatalf("build-animation functions = %v, want %v", got, want)
	}
	for pc, want := range map[int]bool{0: true, 6: false, 12: false, 18: true, 40: true, 41: false, 53: false, 54: true, 71: true} {
		if got := prog.inBuildAnimation(pc); got != want {
			t.Errorf("word %d in build animation = %v, want %v", pc, got, want)
		}
	}
	if len(engineCallbackNames) != 40 {
		t.Fatalf("%d engine callback names, want the forty of [04 R-CB-01 §1]", len(engineCallbackNames))
	}
	// A program assembled without Load has no derived set.
	if (&Program{Code: prog.Code, Scripts: prog.Scripts, ScriptsByID: prog.ScriptsByID}).inBuildAnimation(18) {
		t.Fatal("a literal program carries a build-animation set")
	}
}

// TestBuilderSpeedScalesOnlyBuildAnimationTiming: inside a build-animation
// function the per-tick turn, move and spin speeds and the spin acceleration
// become value·k/30 and a sleep dur·30/(1000·k), truncated toward zero; the
// shared function keeps retail's value/30 and dur·30/1000 [04 §4.6].
func TestBuilderSpeedScalesOnlyBuildAnimationTiming(t *testing.T) {
	for _, tc := range []struct {
		k                                                int32
		turn, move, sleep, spinSpeed, spinAccel, stopAcc int32
	}{
		{1, 10, 3, 30, 20, 2, -5},
		{5, 50, 15, 6, 100, 10, -25},
		{20, 200, 60, 1, 400, 40, -100},
	} {
		prog := builderSpeedProgram(t)
		vm := newTestVM(prog)
		vm.SetBuilderSpeed(tc.k)
		if !vm.StartByName("StartBuilding", nil) {
			t.Fatal("StartBuilding did not start")
		}
		vm.Drain(1)
		request := &vm.Threads[1]
		if request.Status != ThreadSleeping || request.Sleep != tc.sleep {
			t.Fatalf("×%d RequestState: status %d sleep %d, want sleeping %d", tc.k, request.Status, request.Sleep, tc.sleep)
		}
		if got := vm.anims[0].axes[1].turnSpeed; got != tc.turn {
			t.Errorf("×%d build turn speed %d, want %d", tc.k, got, tc.turn)
		}
		if got := vm.anims[0].axes[0].moveSpeed; got != tc.move {
			t.Errorf("×%d build move speed %d, want %d", tc.k, got, tc.move)
		}
		// Shared runs at retail timing whoever calls it.
		for i := 0; i < 40 && vm.anims[0].axes[2].turnSpeed == 0; i++ {
			vm.Drain(1)
		}
		shared := -1
		for i := range vm.Threads {
			if vm.Threads[i].Status == ThreadSleeping && vm.Threads[i].PC >= 41 && vm.Threads[i].PC < 54 {
				shared = i
			}
		}
		if shared < 0 || vm.Threads[shared].Sleep != 30 || vm.anims[0].axes[2].turnSpeed != 10 {
			t.Fatalf("×%d Shared: thread %d, turn speed %d, want 10 and a 30-tick sleep", tc.k, shared, vm.anims[0].axes[2].turnSpeed)
		}

		spin := newTestVM(prog)
		spin.SetBuilderSpeed(tc.k)
		spin.StartByName("Deactivate", nil)
		spin.Drain(1)
		if a := spin.anims[0].axes[0]; a.spinTarget != tc.spinSpeed || a.spinAccel != tc.spinAccel {
			t.Errorf("×%d spin target %d accel %d, want %d %d", tc.k, a.spinTarget, a.spinAccel, tc.spinSpeed, tc.spinAccel)
		}
		// The ramp reaches the scaled target in the same ten visits at every
		// factor; the stop-spin then writes the scaled deceleration, which
		// one visit of the ramp leaves standing.
		for i := 0; i < 11; i++ {
			spin.Drain(1)
		}
		spin.StartByName("Activate", nil)
		spin.Drain(1)
		if a := spin.anims[0].axes[0]; a.spinAccel != tc.stopAcc || a.turnSpeed != tc.spinSpeed+tc.stopAcc {
			t.Errorf("×%d stop-spin acceleration %d speed %d, want %d %d", tc.k, a.spinAccel, a.turnSpeed, tc.stopAcc, tc.spinSpeed+tc.stopAcc)
		}
	}
}

// TestBuilderSpeedIdentityIsByteForByte: a VM at the identity — never set,
// set to 0 or set to 1 — runs exactly the retail VM, and a factor changes
// nothing in a program with no build-animation function.
func TestBuilderSpeedIdentityIsByteForByte(t *testing.T) {
	run := func(prog *Program, k int32, entries ...string) [][]byte {
		vm := NewVM(prog)
		if k >= 0 {
			vm.SetBuilderSpeed(k)
		}
		for _, name := range entries {
			vm.StartByName(name, nil)
		}
		var out [][]byte
		for i := 0; i < 50; i++ {
			vm.Drain(1)
			out = append(out, checkpointVMBytes(t, vm))
		}
		return out
	}
	prog := builderSpeedProgram(t)
	entries := []string{"StartBuilding", "Create", "AimPrimary", "Deactivate", "Activate"}
	base := run(prog, -1, entries...)
	for _, k := range []int32{0, 1} {
		got := run(prog, k, entries...)
		for i := range base {
			if !bytes.Equal(base[i], got[i]) {
				t.Fatalf("factor %d diverged from the unset VM at drain %d", k, i)
			}
		}
	}
	// Create and AimPrimary reach only Shared, which keeps retail timing.
	plain := run(prog, -1, "Create", "AimPrimary")
	fast := run(prog, 20, "Create", "AimPrimary")
	for i := range plain {
		if !bytes.Equal(plain[i], fast[i]) {
			t.Fatalf("a factor changed code outside the build animations at drain %d", i)
		}
	}
	if fast := run(prog, 5, entries...); bytes.Equal(fast[0], base[0]) {
		t.Fatal("the fixture must show the factor, or the comparison proves nothing")
	}
}

// TestStockStateRequestLoopIsABuildAnimation: the stock construction kbots'
// and vehicles' shared open/close request loop is reached from the build and
// activation callbacks and not from Create, which only initialises its state,
// so Builder speed applies to it; nothing Create starts is in the set
// (docs/DESIGN_MODS_MUTATORS.md "Builder speed"). Skipped without retail
// assets.
func TestStockStateRequestLoopIsABuildAnimation(t *testing.T) {
	root := testsupport.RetailRoot(t)
	fs := vfs.New()
	if err := fs.MountGameDirectory(root); err != nil {
		t.Fatalf("mount: %v", err)
	}
	defer fs.Close()
	for _, unit := range []string{"armck", "armcv", "corck", "corcv"} {
		prog, found, err := LoadFromFS(fs, unit)
		if err != nil || !found {
			t.Fatalf("%s: stock script %v, %v", unit, found, err)
		}
		set := buildAnimationFunctions(prog)
		if !slices.Contains(set, "RequestState") || !slices.Contains(set, "StartBuilding") {
			t.Errorf("%s build animations %v miss the request loop", unit, set)
		}
		for _, name := range []string{"Create", "InitState", "SmokeUnit", "StartMoving", "StopMoving"} {
			if slices.Contains(set, name) {
				t.Errorf("%s build animations %v hold %s", unit, set, name)
			}
		}
	}
}

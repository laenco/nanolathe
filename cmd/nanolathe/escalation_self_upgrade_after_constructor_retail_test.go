//go:build retail

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/testsupport"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// A building that a constructor finishes has been allocated as a nanoframe, and
// the allocation clears the script-owned build-stance bits ahead of `Create`
// [05 "Nanoframe allocation"]. Escalation authors self-upgrades as an ordinary
// factory product and raises `INBUILDSTANCE` in `Create`, so a clear that lands
// after `Create` leaves the unit's first work order parked in the phase-1 stance
// wait forever — the upgrade is queued, never worked, and nothing is spent,
// because the only later raise lives in `StopBuilding`, which a build that never
// starts cannot reach [04 R-COB-06][05 R-FAC-02 §4].
func TestEscalationSelfUpgradeAfterConstructorBuild(t *testing.T) {
	roots := filepath.SplitList(os.Getenv("NANOLATHE_MOD_ROOTS_ESCALATION"))
	if len(roots) == 0 {
		t.Skip("Escalation content roots not supplied")
	}
	opts := Options{Root: testsupport.RetailRoot(t), Roots: append([]string{testsupport.RetailRoot(t)}, roots...), Map: "expanded confluence", Seed: 7, ModConfig: modRootsConfigPath(t, "escalation")}
	cs, err := openContent(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	cfg := session.DirectSkirmishConfig(opts.Map)
	cfg.ApplyDefaults()
	cfg.Gameplay = gameplay.Modern
	sess, cat, err := newBattleSessionWithConfig(opts, cs, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var commander *units.Unit
	for _, u := range sess.Units.IterSliced() {
		if u != nil && u.Def != nil && u.Def.Commander && u.Owner == uint8(sess.LocalOwner) {
			commander = u
			break
		}
	}
	if commander == nil {
		t.Fatal("no local commander")
	}
	if _, ok := cat.Unit("corasp"); !ok {
		t.Fatal("corasp absent from the Escalation content")
	}
	if err := sess.EnqueueHumanCommand(session.HumanCommand{Kind: session.HumanMobileBuild, MobileBuild: session.HumanMobileBuildCommand{
		Builder: commander.Handle, Product: "corasp",
		WX: commander.X + numeric.FixedFromInt(96), WZ: commander.Z,
	}}); err != nil {
		t.Fatal(err)
	}
	fund := func() {
		p := &sess.Econ.Players[sess.LocalOwner]
		p.Stock, p.Capacity = [2]float32{1e7, 1e7}, [2]float32{1e7, 1e7}
	}
	var pad *units.Unit
	completed := false
	for step := int32(1); step <= 6000 && !completed; step++ {
		fund()
		sess.Step(step)
		if pad == nil {
			for _, u := range sess.Units.IterSliced() {
				if u != nil && u.Def != nil && u.Def.CanonicalKey == "corasp" {
					pad = u
					break
				}
			}
			continue
		}
		completed = pad.Remaining == 0
	}
	if pad == nil || !completed {
		t.Fatal("the commander never finished the Repair Pad inside 6000 ticks")
	}
	if !pad.InBuildStance {
		t.Fatal("a constructor-built building lost the build stance its own Create raised: the nanoframe clear ran after Create")
	}
	if err := sess.EnqueueHumanCommand(session.HumanCommand{Kind: session.HumanFactoryBuild, FactoryBuild: session.HumanFactoryBuildCommand{Builder: pad.Handle, Product: "corasp_upgrade"}}); err != nil {
		t.Fatal(err)
	}
	// 3000 ticks covers the funded horizon of this product (BuildTime 14390 at
	// floor(WorkerTime 540 / 30) = 18 construction units per step).
	for step := int32(1); step <= 3000; step++ {
		fund()
		sess.Step(step)
		for _, u := range sess.Units.IterSliced() {
			if u == nil || u.Def == nil || u.Def.CanonicalKey != "corasp_upgrade" {
				continue
			}
			if u.Remaining == 0 && u.Attachment.Carrier == pad.Handle {
				return
			}
		}
	}
	t.Fatal("the constructor-built Repair Pad's self-upgrade never finished")
}

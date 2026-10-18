//go:build retail

package main

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/client"
	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/hud"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/testsupport"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// The Aegis authors Builder=0 but has an ordinary numbered GUI containing a
// mobile upgrade product. A named counted-product button uses that selected
// unit's queue independently of mobile-build capability [07 R-P0-11 §1].
func TestEscalationAegisUpgradeFromAuthoredMenu(t *testing.T) {
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
	for _, a := range sess.AI {
		if a != nil {
			for j := range a.Deadlines {
				a.Deadlines[j] = 1 << 30
			}
		}
	}
	windowCtx := newBattleWindowContext(cs, nil)
	windowCtx.completeTransition()
	hudView, err := loadRetailBattleHUD(cs.fs, sess, cat, retailPaletteForTest(t, cs), nil, windowCtx)
	if err != nil {
		t.Fatal(err)
	}
	cl, err := client.New(client.Options{Width: 1280, Height: 768})
	if err != nil {
		t.Fatal(err)
	}
	b := &battleSession{sess: sess, cat: cat, cl: cl, hud: hudView}
	def, ok := cat.Unit("ARMSHGEN")
	if !ok || def.Builder {
		t.Fatal("fixture must retain the Aegis's authored non-builder flag")
	}
	x, z := numeric.FixedFromInt(600), numeric.FixedFromInt(600)
	h, err := sess.Units.Create(def, sess.LocalOwner, x, sess.World.HeightAt(x, z), z)
	if err != nil {
		t.Fatal(err)
	}
	sess.CompleteUnit(h)
	provider := sess.Units.Unit(h)
	// The shown page is the client's local interface state; the committed frame
	// publishes it (DESIGN_MULTIPLAYER §7.3).
	provider.Flags = hud.EncodePageBits(provider.Flags, 1)
	recipientDef, ok := cat.Unit("ARMMEX")
	if !ok {
		t.Fatal("missing extractor")
	}
	rx := x + numeric.FixedFromInt(500)
	rh, err := sess.Units.Create(recipientDef, sess.LocalOwner, rx, sess.World.HeightAt(rx, z), z)
	if err != nil {
		t.Fatal(err)
	}
	sess.CompleteUnit(rh)
	recipient := sess.Units.Unit(rh)
	// Selection is local state too, so the frame only carries it once the
	// client's interface has been advanced by stepping.
	b.local = testSelection(provider)
	for step := int32(1); step <= 5; step++ {
		sess.Step(step)
	}
	if recipient.Armored {
		t.Fatal("extractor outside the base radius is already protected")
	}
	// The frame the HUD reads is the committed one with the client's local
	// interface composed in; the raw sim snapshot carries no selection or page.
	f, _ := b.currentSnapshot()
	if f == nil {
		t.Fatal("no committed frame")
	}
	w, _, err := b.hud.windowForRequired(b, f)
	if err != nil {
		t.Fatal(err)
	}
	if w == nil {
		t.Fatalf("Aegis's numbered GUI was not loaded: builder=%d page=%d pageCount=%d selection=%v", f.CommandPage.Builder, f.CommandPage.Page, f.CommandPage.PageCount, f.Selection.Handles)
	}
	capture := func(name string) {
		t.Helper()
		dir := os.Getenv("NANOLATHE_MENU_SHOTS")
		if dir == "" {
			return
		}
		cl.SetUIStage(battleHUDUIStage{hud: b.hud, battle: b})
		cl.SetSnapshot(sess.Snapshot)
		cl.SetPalette(retailPaletteForTest(t, cs))
		f, err := os.Create(filepath.Join(dir, "escalation-aegis-"+name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(f, cl.ComposeFrame())
		closeErr := f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	capture("menu")
	clicked := false
	for i, g := range w.Gadgets {
		if !strings.EqualFold(g.Name, "ARMSHGEN_UPG") {
			continue
		}
		r := w.PlacedRect(i)
		if !hudConsumeClick(b.hud, b, r.X+r.W/2, r.Y+r.H/2) {
			t.Fatal("authored upgrade click was not consumed")
		}
		clicked = true
		break
	}
	if !clicked {
		t.Fatal("Aegis's authored upgrade gadget is absent")
	}
	pending := sess.PendingHumanCommands()
	if len(pending) != 1 || pending[0].Kind != session.HumanFactoryBuild || pending[0].FactoryBuild.Builder != h || pending[0].FactoryBuild.Product != "armshgen_upg" {
		t.Fatalf("upgrade click did not queue the authored product: %+v; diagnostic=%v", pending, b.hud.LastDispatchError())
	}
	// Let the real product and parent scripts finish construction and retain
	// the upgrade. The extractor is outside the base radius (377.5), inside
	// the completed upgrade radius (570) [research/extensions/escalation-shields.md
	// "Coverage discovery and recipient behavior"].
	//
	// Coverage is the recipient's own scan, so a provider that arrives after the
	// recipient has already polled is seen at that scan's next visit, and the
	// authored delay for a recipient whose last scan recognized no nearby source
	// is 200-300 s — 6000-9000 logical ticks at the 30 Hz tick. Measured here:
	// the extractor flips 6681 ticks after an upgrade marker appears next to it,
	// inside that band [research/extensions/escalation-shields.md "ARMMEX
	// polling"]. The budget below is the construction plus that wait.
	const repollBudget = 9500
	fund := func() {
		p := &sess.Econ.Players[0]
		p.Stock, p.Capacity = [2]float32{1e6, 1e6}, [2]float32{1e6, 1e6}
	}
	builtAt, coveredAt := -1, -1
	for tick := 0; coveredAt < 0 && tick < 40000; tick++ {
		fund()
		sess.Step(sess.Clock.ScaledAnchor + 1)
		if tick == 0 {
			capture("queued")
		}
		if len(provider.Attachment.Cargo) != 1 {
			continue
		}
		upgrade := sess.Units.Unit(provider.Attachment.Cargo[0])
		if upgrade == nil || upgrade.Def.CanonicalKey != "armshgen_upg" || upgrade.Remaining != 0 || upgrade.Attachment.Carrier != h {
			continue
		}
		if builtAt < 0 {
			builtAt = tick
			capture("built")
		}
		if recipient.Armored {
			coveredAt = tick
			break
		}
		if tick-builtAt > repollBudget {
			break
		}
	}
	if coveredAt < 0 {
		upgradeState := "none"
		if len(provider.Attachment.Cargo) == 1 {
			if u := sess.Units.Unit(provider.Attachment.Cargo[0]); u != nil {
				upgradeState = fmt.Sprintf("def=%s remaining=%.4f carrier=%d alive=%v dying=%v", u.Def.CanonicalKey, u.Remaining, u.Attachment.Carrier, u.Alive, u.Dying)
			} else {
				upgradeState = "cargo unit missing"
			}
		}
		t.Fatalf("Aegis upgrade did not extend coverage: builtAt=%d cargo=%+v upgrade{%s} armor=%v diagnostics=%v", builtAt, provider.Attachment.Cargo, upgradeState, recipient.Armored, provider.GetScript().Diagnostics())
	}
	for _, u := range []*units.Unit{provider, sess.Units.Unit(provider.Attachment.Cargo[0]), recipient} {
		if diags := u.GetScript().Diagnostics(); len(diags) != 0 {
			t.Fatalf("%s diagnostics: %v", u.Def.UnitName, diags)
		}
	}
	t.Logf("the upgraded Aegis covered the extractor %d ticks (about %.0f s) after the upgrade finished", coveredAt-builtAt, float32(coveredAt-builtAt)/30)
	capture("complete")
}

package session

import (
	"reflect"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/combat"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/economy"
	"github.com/nanolathe-gg/nanolathe/internal/frame"
	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/hud"
	"github.com/nanolathe-gg/nanolathe/internal/mission"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/save"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// Added faction names are content metadata in every gameplay mode. Stock
// registration keeps its established spelling [08 R-SKIR-01 §2].
func TestSkirmishPlayerNamesUseAddedSideMetadata(t *testing.T) {
	for _, mode := range []gameplay.Mode{gameplay.Modern, gameplay.Strict31, gameplay.Community39} {
		s := &Session{Gameplay: mode, Catalog: strictMinimalCatalog()}
		s.Catalog.Sides = append(s.Catalog.Sides, &content.SideDef{Name: "TALON"}, &content.SideDef{Name: "Rumad"})
		for _, tc := range []struct {
			controller uint8
			side       int
			want       string
		}{{1, 2, "Player"}, {2, 0, "Arm"}, {2, 1, "Core"}, {2, 2, "TALON"}, {2, 3, "Rumad"}, {2, 4, ""}} {
			if got := s.skirmishPlayerName(tc.controller, tc.side); got != tc.want {
				t.Fatalf("%s controller %d side %d name = %q, want %q", mode, tc.controller, tc.side, got, tc.want)
			}
		}
		// A total-conversion mod may replace the base ordinal's definition.
		// Its authored identity must not inherit the stock label.
		for side, name := range []string{"TALON", "Rumad"} {
			s.Catalog.Sides[side].Name = name
			if got := s.skirmishPlayerName(2, side); got != name {
				t.Fatalf("%s replaced side %d name = %q, want %q", mode, side, got, name)
			}
		}
	}
}

// Name and rank are absent from the closed retail account. Restoration must
// register the saved slots before the score panel walks them: duplicate zero
// ranks hide every player except the first [07 R-HUD-04 §1]. Sparse slots and
// players with no units still retain distinct rows.
func TestRestoredPlayerRowsKeepDistinctRanksAndNames(t *testing.T) {
	s, _ := newRestoreCoreFixture(t, 0)
	s.Mission = &mission.Mission{Type: mission.TypeSkirmish}
	s.Catalog = strictMinimalCatalog()
	s.Catalog.Sides = append(s.Catalog.Sides, &content.SideDef{Name: "TALON"})
	s.SeedSessionRNG(7, 9)
	simDraws, crtDraws := s.SimRNG().Draws(), s.CrtRNG().Draws()
	b := save.NewBuilder()
	want := []save.PlayerSlot{
		{Index: 0, Controller: 1, Side: 0, Logo: 2, Kills: 3, Losses: 1, Energy: 123, Metal: 45},
		{Index: 3, Controller: 2, Side: 2, Logo: 7, Kills: 12, Losses: 8, Energy: 456, Metal: 67},
		{Index: 9, Controller: 2, Side: 1, Logo: 4, Kills: 6, Losses: 2, Energy: 789, Metal: 89},
	}
	for _, p := range want {
		save.WritePlayerSlot(b, p)
	}
	bank, err := save.OpenBytes(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	image := &save.BattleImage{}
	for _, p := range want {
		saved, ok := save.ReadPlayerSlot(bank, p.Index)
		if !ok {
			t.Fatalf("player %d account missing", p.Index)
		}
		image.Players = append(image.Players, saved)
	}
	// An explicitly supplied name is presentation state and is preserved.
	s.Econ.Players[9].Name = "Named computer"
	if err := RestoreRetailBattleCore(&RetailBattleStage{Session: s, Image: image}); err != nil {
		t.Fatal(err)
	}
	var published frame.Frame
	publishPlayerRows(s, &published)
	var slots [frame.PlayerRowSlots]hud.ScoreSlot
	for i, row := range published.Players {
		slots[i] = hud.ScoreSlot{Present: row.Present, Controller: row.Controller, Side: row.Side, LiveUnits: row.LiveUnits, Auxiliary: row.Auxiliary, Watcher: row.Watcher, Rank: row.Rank}
	}
	order, _ := hud.ScoreRowOrder(slots[:], len(want))
	if !reflect.DeepEqual(order, []int{0, 3, 9}) {
		t.Fatalf("restored score rows = %v, want all saved slots in registration order", order)
	}
	for i, name := range []string{"Player", "TALON", "Named computer"} {
		slot := want[i].Index
		if got := published.Players[slot].Name; got != name {
			t.Errorf("slot %d name = %q, want %q", slot, got, name)
		}
	}
	for _, p := range want {
		row := published.Players[p.Index]
		if row.Rank != uint8(p.Index) || row.Logo != p.Logo || row.Side != p.Side || row.Controller != p.Controller || row.Kills != int(p.Kills) || row.Losses != int(p.Losses) {
			t.Errorf("slot %d changed saved row fields: %+v", p.Index, row)
		}
		if got := s.Econ.Players[p.Index].Stock; got[economy.Energy] != p.Energy || got[economy.Metal] != p.Metal {
			t.Errorf("slot %d changed saved stocks: %v", p.Index, got)
		}
	}
	if s.SimRNG().Draws() != simDraws || s.CrtRNG().Draws() != crtDraws {
		t.Fatal("player restoration or publication drew from an RNG")
	}
}

// publishedPlayerRows publishes one snapshot and returns the committed rows.
func publishedPlayerRows(t *testing.T, s *Session, tick uint32) [frame.PlayerRowSlots]frame.PlayerRow {
	t.Helper()
	s.publishSnapshot(tick)
	cur := s.Snapshot.Current()
	if cur == nil {
		t.Fatalf("no committed frame at tick %d", tick)
	}
	return cur.Players
}

// The rows carry the row filter's terms for every one of the ten slots, on
// every tick of a live battle — not only once a result latches
// [07 R-HUD-04 §1].
func TestPlayerRowsPublishTheRowFilterTermsEveryTick(t *testing.T) {
	s := newLoopTestSession(t, 2)
	// The helper builds the two player records directly rather than through a
	// registration path, so the rank half of registration is applied here
	// [08 R-SKIR-01 §2].
	for i := 0; i < 2; i++ {
		s.Econ.Players[i].SeedScorePanelRank(i)
	}
	for tick := uint32(1); tick <= 3; tick++ {
		rows := publishedPlayerRows(t, s, tick)
		if !rows[0].Present || !rows[1].Present {
			t.Fatalf("tick %d: occupied slots not published as present: %+v", tick, rows[:2])
		}
		// newLoopTestSession gives slot 0 controller 1 and slot 1 controller 2.
		if rows[0].Controller != 1 || rows[1].Controller != 2 {
			t.Fatalf("tick %d: controller bytes = %d/%d, want 1/2", tick, rows[0].Controller, rows[1].Controller)
		}
		if rows[0].LiveUnits != 1 || rows[1].LiveUnits != 1 {
			t.Fatalf("tick %d: live-unit counts = %d/%d, want 1/1", tick, rows[0].LiveUnits, rows[1].LiveUnits)
		}
		for i := 2; i < frame.PlayerRowSlots; i++ {
			// An unoccupied slot publishes its zero row. It was never
			// registered, so its rank byte is the zero its record was allocated
			// with, not the slot index [08 R-SKIR-01 §2]; the candidate scan's
			// present test is what keeps that zero out of the ranking
			// [06 §12.1 R-WPN-02 §9].
			if rows[i] != (frame.PlayerRow{}) {
				t.Fatalf("tick %d: unoccupied slot %d published %+v, want the zero row", tick, i, rows[i])
			}
		}
		// The published byte is the record's, and no kill has been credited, so
		// it is still the registration value [08 R-SKIR-01 §2][07 R-HUD-04 §1].
		if rows[0].Rank != 0 || rows[1].Rank != 1 {
			t.Fatalf("tick %d: rank bytes = %d/%d, want the registration values 0/1", tick, rows[0].Rank, rows[1].Rank)
		}
	}
}

// A death credited through the finalizer moves the crediting slot's kill
// counter and the victim slot's loss counter, and the rows carry both on the
// next publication [06 §12.1][08 R-CAMP-01 §7][07 R-HUD-04 §1].
func TestPlayerRowsTrackKillsAndLossesAcrossAScriptedKill(t *testing.T) {
	s := newLoopTestSession(t, 2)
	before := publishedPlayerRows(t, s, 1)
	if before[0].Kills != 0 || before[1].Losses != 0 {
		t.Fatalf("counters were not zero before the kill: %+v", before[:2])
	}

	victim := s.Units.IterSliced()[1]
	if victim.Owner != 1 {
		t.Fatalf("fixture victim owner = %d, want 1", victim.Owner)
	}
	// A packet from slot 0 with an ordinary cause is the full credit path.
	victim.LastDamageCause = uint8(combat.CauseOrdinary)
	victim.LastDamageSide = 0
	s.Units.Destroy(pool.Handle(victim.Handle), units.DeathKilled)
	s.stepUnitPhase(2)

	after := publishedPlayerRows(t, s, 2)
	if after[0].Kills != 1 {
		t.Fatalf("crediting slot kills = %d, want 1", after[0].Kills)
	}
	if after[1].Losses != 1 {
		t.Fatalf("victim slot losses = %d, want 1", after[1].Losses)
	}
	if after[1].LiveUnits != 0 {
		t.Fatalf("victim slot live-unit count = %d, want 0", after[1].LiveUnits)
	}
	// The rows are a copy of the authoritative counters, never a rescan.
	for i := 0; i < frame.PlayerRowSlots; i++ {
		p := s.Econ.Players[i]
		if after[i].Kills != int(p.Kills) || after[i].Losses != int(p.Losses) ||
			after[i].CommandersKilled != int(p.CommanderKills) || after[i].CommandersLost != int(p.CommanderLosses) {
			t.Fatalf("slot %d row counters %+v do not match the player record %d/%d/%d/%d",
				i, after[i], p.Kills, p.Losses, p.CommanderKills, p.CommanderLosses)
		}
	}
}

// Cursor prediction receives the same directional alliance row as command
// resolution. The reverse declaration is deliberately different here: a
// publication must not quietly symmetrize it [04 §3.4][05 R-SHARE-01 §1][I6].
func TestPlayerRowsPublishDirectionalAllianceRows(t *testing.T) {
	s := newLoopTestSession(t, 3)
	s.Econ.Players[0].Allies[1] = true
	s.Econ.Players[1].Allies[0] = false
	s.Econ.Players[1].Allies[2] = true

	rows := publishedPlayerRows(t, s, 1)
	if !rows[0].Allies[1] {
		t.Fatal("slot 0 alliance row omitted its declaration toward slot 1")
	}
	if rows[1].Allies[0] {
		t.Fatal("slot 1 alliance row inherited slot 0's reverse declaration")
	}
	if !rows[1].Allies[2] {
		t.Fatal("slot 1 alliance row omitted its independent declaration toward slot 2")
	}
	// Publication owns a value copy. A later mutation cannot rewrite the
	// already-committed frame that presentation may still be reading [I6].
	s.Econ.Players[0].Allies[1] = false
	if !rows[0].Allies[1] {
		t.Fatal("saved player row changed after live alliance mutation")
	}
	after := publishedPlayerRows(t, s, 2)
	if after[0].Allies[1] {
		t.Fatal("next player row did not reflect the later alliance mutation")
	}
}

// Two identical runs publish identical rows: the publication reads only
// authoritative state and iterates slots 0..9 ascending [I1][I6].
func TestPlayerRowsAreStableAcrossTwoIdenticalRuns(t *testing.T) {
	run := func() [frame.PlayerRowSlots]frame.PlayerRow {
		s := newLoopTestSession(t, 2)
		var rows [frame.PlayerRowSlots]frame.PlayerRow
		for tick := uint32(1); tick <= 4; tick++ {
			rows = publishedPlayerRows(t, s, tick)
		}
		return rows
	}
	a, b := run(), run()
	if a != b {
		t.Fatalf("identical runs published different rows:\n a=%+v\n b=%+v", a, b)
	}
}

// This checks publication against the partial fingerprint and both RNG draw
// counts; the fingerprint does not cover every simulation owner [I4][I6].
func TestPlayerRowPublicationPreservesPartialFingerprintAndDrawCounts(t *testing.T) {
	s := newLoopTestSession(t, 2)
	s.publishSnapshot(1)
	hashBefore, err := s.PartialStateFingerprint()
	if err != nil {
		t.Fatalf("PartialStateFingerprint: %v", err)
	}
	simBefore, crtBefore := s.SimRNG().Draws(), s.CrtRNG().Draws()
	published := s.Snapshot.BeginWrite()
	publishPlayerRows(s, published)
	publishPlayerRows(s, published)
	hashAfter, err := s.PartialStateFingerprint()
	if err != nil {
		t.Fatalf("PartialStateFingerprint: %v", err)
	}
	if hashAfter != hashBefore {
		t.Fatalf("partial fingerprint changed across the publication: %s -> %s", hashBefore, hashAfter)
	}
	if s.SimRNG().Draws() != simBefore || s.CrtRNG().Draws() != crtBefore {
		t.Fatalf("draw counts changed: sim %d->%d, crt %d->%d",
			simBefore, s.SimRNG().Draws(), crtBefore, s.CrtRNG().Draws())
	}
}

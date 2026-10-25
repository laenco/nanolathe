package session

import (
	"encoding/binary"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/cob"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/save"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// The complete projection/bank/staging boundary must register every saved
// player before publishing its row [08 "Player records"][07 R-HUD-04 §1].
func TestRetailStagingRegistersSavedScoreRows(t *testing.T) {
	f := loadRetailFixture(t)
	f.cfg.Gameplay = gameplay.Strict31
	f.cfg.NumPlayers = 3
	f.cfg.Players[2].Controller = SkirmishControllerComputer
	f.cfg.Players[2].Side = 0
	f.cfg.Players[2].Color = 3
	src := f.session(t)
	t.Cleanup(src.closeAIControllers)
	in, err := src.RetailBattleSaveInputs(RetailBattleSummary(src, "score rows", "0", SkirmishDefaultUnitLimit), save.Camera{})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := src.RetailProjection(in)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := projection.Build()
	if err != nil {
		t.Fatal(err)
	}
	bank, err := save.OpenBytes(builder.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	stage, err := StageRetailBattle(bank, RetailLoadDeps{FS: f.fs, Catalog: f.cat, Gameplay: gameplay.Strict31, UnitLimit: src.Skirmish.UnitLimit})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stage.Session.closeAIControllers)
	check := func() {
		t.Helper()
		for i, name := range []string{"Player", "Core", "Arm"} {
			p := stage.Session.Econ.Players[i]
			if !p.Exists || p.Rank != uint8(i) || p.Name != name {
				t.Errorf("saved slot %d registration = present:%v rank:%d name:%q", i, p.Exists, p.Rank, p.Name)
			}
		}
	}
	check()
	if err := RestoreRetailBattleCore(stage); err != nil {
		t.Fatal(err)
	}
	check()
	rows := publishedPlayerRows(t, stage.Session, stage.Session.Clock.GlobalTick)
	for i := 0; i < 3; i++ {
		if !rows[i].Present || rows[i].Rank != uint8(i) || rows[i].Name != stage.Session.Econ.Players[i].Name {
			t.Errorf("saved slot %d published incomplete score row: %+v", i, rows[i])
		}
	}
}

func TestReserveRetailUnitsValidatesBeforeAllocation(t *testing.T) {
	def := &content.UnitDef{UnitName: "armcom", Script: &cob.Program{Code: []uint32{0x10065000}, Scripts: map[string]int{}}}
	cat := &content.Catalog{Units: map[string]*content.UnitDef{"armcom": def}}
	w := units.NewSliced(1, cat)
	record := save.UnitRecord{Number: 0, StableID: 1, Data: make([]byte, save.UnitBoxSize)}
	copy(record.Data, []byte("armcom"))
	record.Data[0x20] = 10 // outside the ten fixed player slices
	before := w.Used()
	if _, err := reserveRetailUnits(w, cat, []save.UnitRecord{record}); err == nil {
		t.Fatal("invalid owner accepted")
	}
	if got := w.Used(); got != before {
		t.Fatalf("failed identity validation allocated %d units", got)
	}
	// A malformed stable identity must be rejected before any later field is
	// inspected; keep this explicit to lock the atomic staging boundary.
	record.Data[0x20] = 0
	binary.LittleEndian.PutUint16(record.Data[0x21:], 0)
	record.StableID = 0
	if _, err := reserveRetailUnits(w, cat, []save.UnitRecord{record}); err == nil {
		t.Fatal("null stable slot accepted")
	}
	if got := w.Used(); got != before {
		t.Fatalf("failed stable identity validation allocated %d units", got)
	}
}

func TestReserveRetailUnitsSkipsCompatibilityNullRecords(t *testing.T) {
	def := &content.UnitDef{UnitName: "armcom", Script: &cob.Program{Code: []uint32{0x10065000}, Scripts: map[string]int{}}}
	cat := &content.Catalog{Units: map[string]*content.UnitDef{"armcom": def}}
	w := units.NewSliced(4, cat)
	standard := func(number int, id uint16) save.UnitRecord {
		data := make([]byte, save.UnitBoxSize)
		copy(data, []byte("armcom"))
		binary.LittleEndian.PutUint16(data[0x21:], id)
		return save.UnitRecord{Number: number, StableID: id, Data: data}
	}
	stable, err := reserveRetailUnits(w, cat, []save.UnitRecord{
		standard(0, 1),
		{Number: 1, Compat: true, Data: make([]byte, save.UnitBoxCompatSize)},
		standard(2, 3),
	})
	if err != nil {
		t.Fatalf("mixed standard/null records: %v", err)
	}
	if w.Used() != 0 || stable[1] != 1 || stable[3] != 3 {
		t.Fatalf("identity reservation constructed units or lost standard slots: used=%d stable=%v", w.Used(), stable)
	}
}

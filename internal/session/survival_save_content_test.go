package session

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/save"
)

func TestSurvivalSaveContentIdentity(t *testing.T) {
	f := loadRetailFixture(t)
	cfg := SurvivalSkirmishConfig(retailMap, 1, SurvivalOptions{})
	cfg.RNGSimSeed, cfg.RNGCrtSeed = 7, 7
	src, err := NewSkirmishWithProgress(f.fs, f.cat, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	want, ok := src.SimulationContentDigest()
	if !ok {
		t.Fatal("fresh Survival has no frozen content identity")
	}
	in, err := src.RetailBattleSaveInputs(RetailBattleSummary(src, "identity", "0", src.Skirmish.UnitLimit), save.Camera{})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "identity.SAV")
	if err := src.WriteRetailSave(path, in); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRetailSavePath(path, RetailLoadDeps{FS: f.fs, Catalog: f.cat, SimSeed: 7, CRTSeed: 7, UnitLimit: cfg.UnitLimit, Survival: RecordSurvival(src)})
	if err != nil {
		t.Fatal(err)
	}
	dst := loaded.Battle.Session
	before, sim, crt := dst.UnitStateChecksum(), *dst.SimRNG(), *dst.CrtRNG()
	inputs, err := dst.captureSurvivalSaveContent(f.fs)
	if err != nil {
		t.Fatal(err)
	}
	if got := inputs.Digest(); got != want {
		fs, _ := src.Units.COBSource()
		original := frozenInputsOf(fs).Manifest()
		restored := inputs.Manifest()
		for i := range min(len(original), len(restored)) {
			if !reflect.DeepEqual(original[i], restored[i]) {
				t.Errorf("content entry %d: original %+v, restored %+v", i, original[i], restored[i])
			}
		}
		t.Fatalf("restored content %x (%d entries), want %x (%d entries)", got, len(restored), want, len(original))
	}
	if before != dst.UnitStateChecksum() || sim != *dst.SimRNG() || crt != *dst.CrtRNG() {
		t.Fatal("computing the host comparison identity changed the battle")
	}
}

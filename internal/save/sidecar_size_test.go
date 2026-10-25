package save

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A normal-pace wave in an inexpensive authored pool can have 131,070 picks
// at eighty minutes. Its saved plan must exceed the old selection-only limit
// and still load (DESIGN_MODS_MUTATORS §7.2, DESIGN_SURVIVAL §11).
func TestSurvivalSidecarCarriesLargeWavePlan(t *testing.T) {
	const picks = 131070
	plan := json.RawMessage(`{"version":1,"plan":{"groups":[{"units":[` + strings.Repeat(`"soldiera",`, picks-1) + `"soldiera"]}]}}`)
	bank := filepath.Join(t.TempDir(), "SURVIVAL.SAV")
	if err := WriteSidecar(bank, Sidecar{Survival: plan}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(SidecarPath(bank))
	if err != nil || info.Size() <= sidecarMaxBytes {
		t.Fatalf("large-wave fixture did not exercise the extended limit: %v, %v", info, err)
	}
	sc, present, err := ReadSidecar(bank)
	if err != nil || !present || sc.Schema != SidecarSchemaSurvival {
		t.Fatalf("read large director: present %t, schema %d, error %v", present, sc.Schema, err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, sc.Survival); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(compact.Bytes(), plan) {
		t.Fatal("large wave changed across the sidecar round trip")
	}
}

// A writer must never report success for a file its reader rejects by size,
// or replace an existing sidecar on that failure. Ordinary schemas keep the
// old limit so their new files remain readable by older builds.
func TestSidecarSizeLimitsAgreeBeforeReplacement(t *testing.T) {
	for _, survival := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "survival"}[survival], func(t *testing.T) {
			bank := filepath.Join(t.TempDir(), "SLOT.SAV")
			original := Sidecar{Profile: "previous"}
			limit := sidecarMaxBytes
			if survival {
				original.Survival = json.RawMessage(`{"version":1}`)
				limit = survivalSidecarMaxBytes
			}
			if err := WriteSidecar(bank, original); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(SidecarPath(bank))
			if err != nil {
				t.Fatal(err)
			}
			oversized := original
			oversized.Profile = strings.Repeat("x", limit)
			if err := WriteSidecar(bank, oversized); err == nil || !strings.Contains(err.Error(), "larger than") {
				t.Fatalf("oversized write error %v", err)
			}
			after, err := os.ReadFile(SidecarPath(bank))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("failed oversized write replaced the previous sidecar")
			}
			body, err := json.Marshal(oversized)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(SidecarPath(bank), body, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, present, err := ReadSidecar(bank); present || err == nil || !strings.Contains(err.Error(), "larger than") {
				t.Fatalf("oversized read: present %t, error %v", present, err)
			}
		})
	}
}

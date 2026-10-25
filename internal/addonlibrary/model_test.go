package addonlibrary

import (
	"path/filepath"
	"testing"
)

// The model layer is selected only by all of an add-on's audited logical
// files: archive names and extensions do not classify it (§5.7).
func TestModelsOnlyAuditsEveryLogicalFile(t *testing.T) {
	for _, tc := range []struct {
		name     string
		archives []map[string]string
		want     bool
	}{
		{"folded model paths", []map[string]string{{"Objects3D/ARMCOM.3DO": "model", "objects3d/parts/gun.3do": "model"}}, true},
		{"multiple archives", []map[string]string{{"objects3d/armcom.3do": "model"}, {"objects3d/corcom.3do": "model"}}, true},
		{"no archives", nil, false},
		{"empty archive", []map[string]string{{}}, false},
		{"other directory", []map[string]string{{"units/armcom.3do": "model"}}, false},
		{"other extension", []map[string]string{{"objects3d/armcom.cob": "script"}}, false},
		{"mixed payload", []map[string]string{{"objects3d/armcom.3do": "model", "units/armcom.fbi": "definition"}}, false},
		{"mixed archives", []map[string]string{{"objects3d/armcom.3do": "model"}, {"textures/armcom.gaf": "textures"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			addon := Addon{ID: "arbitrary", Archives: []Archive{{Name: "rev31.gp3"}}}
			for i, files := range tc.archives {
				name := []string{"ordinary.ufo", "rev31.gp3"}[i]
				write(t, root, name, ufo(t, files))
				addon.Paths = append(addon.Paths, filepath.Join(root, name))
			}
			if got, err := ModelsOnly(addon); err != nil || got != tc.want {
				t.Fatalf("ModelsOnly = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestModelsOnlyReportsUnreadableArchives(t *testing.T) {
	for _, name := range []string{"missing.ufo", "invalid.ufo"} {
		root := t.TempDir()
		if name == "invalid.ufo" {
			write(t, root, name, []byte("not an archive"))
		}
		if got, err := ModelsOnly(Addon{Paths: []string{filepath.Join(root, name)}}); got || err == nil {
			t.Fatalf("%s: ModelsOnly = %v, %v; want false and an error", name, got, err)
		}
	}
}

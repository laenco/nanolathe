package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/addonlibrary"
	"github.com/nanolathe-gg/nanolathe/internal/modlibrary"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

// A room retains the local model layer for both library and folder copies,
// including every archive of one add-on. Its mod's layout and own overrides
// apply as they do offline (DESIGN_MODS_MUTATORS §5.7, §5.8).
func TestRoomModelAddonsKeepTheirLayerAndLayout(t *testing.T) {
	for _, source := range []string{"mounted", "library", "folder"} {
		for _, renamed := range []bool{false, true} {
			name := source
			if renamed {
				name += "/renamed"
			}
			t.Run(name, func(t *testing.T) {
				t.Setenv("XDG_DATA_HOME", t.TempDir())
				base := roomTestBase(t)
				modelDir := "objects3d"
				if renamed {
					modelDir = "modelsM"
				}
				writeRoomArchive(t, base, "rev31.gp3",
					vfs.ArchiveFile{Path: modelDir + "/shared.3do", Data: []byte("patch model")})
				writeRoomArchive(t, base, "bonus.ufo",
					vfs.ArchiveFile{Path: modelDir + "/shared.3do", Data: []byte("bonus model")})
				dir := t.TempDir()
				if source == "folder" {
					dir = base
				}
				addon := addonlibrary.Addon{ID: "models", Name: "Models", Version: "1"}
				for _, file := range []string{"shared", "second"} {
					archive := file + ".ufo"
					writeRoomArchive(t, dir, archive, vfs.ArchiveFile{Path: "objects3d/" + file + ".3do", Data: []byte("replacement " + file)})
					path := filepath.Join(dir, archive)
					size, digest, err := archiveDigest(path)
					if err != nil {
						t.Fatal(err)
					}
					addon.Paths = append(addon.Paths, path)
					addon.Archives = append(addon.Archives, addonlibrary.Archive{Name: archive, Size: int64(size), SHA256: digest})
				}
				cs := roomTestInstall(t, base)
				if renamed {
					cs.layout = vfs.NewLayout(map[string]string{"objects3d": modelDir})
				}
				known := []knownAddon{{id: addon.ID, version: addon.Version, name: addon.Name, archives: addon.Archives}}
				if source == "mounted" {
					cs.addons = []addonlibrary.Addon{addon}
				} else if source == "library" {
					known[0].installed = &addon
				}
				local, err := baseArchives(cs)
				if err != nil {
					t.Fatal(err)
				}
				entries := []session.MatchContentEntry{addonRoomEntry(addon.ID, addon.Version, addon.Archives)}
				for _, a := range local {
					if a.entry.Name != "shared.ufo" && a.entry.Name != "second.ufo" {
						entries = append(entries, a.entry)
					}
				}
				plan := resolveRoomContent(cs, local, nil, known, entries)
				if len(plan.missing) != 0 || len(plan.addons) != 1 || len(plan.addons[0].Paths) != 2 {
					t.Fatalf("model add-on resolution = %+v", plan)
				}
				check := func(want string) {
					t.Helper()
					mounted, view, err := roomContentFS(cs, plan)
					if err != nil {
						t.Fatal(err)
					}
					defer mounted.Close()
					for path, want := range map[string]string{"objects3d/shared.3do": want, "objects3d/second.3do": "replacement second"} {
						if data, err := view.ReadFileLimit(path, 1024); err != nil || string(data) != want {
							t.Fatalf("room %s = %q, %v; want %q", path, data, err, want)
						}
					}
				}
				check("replacement shared")
				modDir := t.TempDir()
				if err := os.MkdirAll(filepath.Join(modDir, modelDir), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(modDir, modelDir, "shared.3do"), []byte("mod model"), 0o600); err != nil {
					t.Fatal(err)
				}
				cs.mod = &modlibrary.Mod{Dir: modDir}
				check("mod model")
			})
		}
	}
}

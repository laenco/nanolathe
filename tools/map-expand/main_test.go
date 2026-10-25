package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/nanolathe-gg/nanolathe/vfs"
)

// Expansion keeps exactly the map-package file classes, byte for byte, and
// refuses case-insensitive duplicates.
func TestMapExpandWritesOnlyPackageFiles(t *testing.T) {
	files := []vfs.ArchiveFile{
		{Path: "maps/Test Map.ota", Data: []byte("[GlobalHeader]")},
		{Path: "maps/Test Map.tnt", Data: []byte("terrain")},
		{Path: "features/tree.tdf", Data: []byte("[tree]{}")},
		{Path: "features/Plants/nested tree.tdf", Data: []byte("[nested tree]{}")},
		{Path: "anims/explo.gaf", Data: []byte("gaf")},
		{Path: "anims/vismasks.gaf", Data: []byte("masks")},
		{Path: "objects3d/x.3do", Data: []byte("3do")},
		{Path: "textures/a.gaf", Data: []byte("tex")},
		{Path: "README.txt", Data: []byte("credits")},
		{Path: "units/armfoo.fbi", Data: []byte("[UNITINFO]")},
		{Path: "sounds/x.wav", Data: []byte("wav")},
		{Path: "docs/readme.txt", Data: []byte("nested")},
	}
	archive := filepath.Join(t.TempDir(), "Test.ufo")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := vfs.WriteArchive(f, files, vfs.ArchiveWriteOptions{Year: 1997}); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "root")
	if err := run(archive, out, false); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"maps/Test Map.ota":               "[GlobalHeader]",
		"maps/Test Map.tnt":               "terrain",
		"features/tree.tdf":               "[tree]{}",
		"features/Plants/nested tree.tdf": "[nested tree]{}",
		"anims/explo.gaf":                 "gaf",
		"objects3d/x.3do":                 "3do",
		"textures/a.gaf":                  "tex",
		"README.txt":                      "credits",
	}
	for name, body := range want {
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(name)))
		if err != nil || string(got) != body {
			t.Fatalf("%s = %q, %v", name, got, err)
		}
	}
	// The written names are the archive's spelling exactly. A case-insensitive
	// host file system would open a folded name above, so compare the tree.
	var written []string
	if err := filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(out, p)
			written = append(written, filepath.ToSlash(rel))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(written)
	var spelled []string
	for name := range want {
		spelled = append(spelled, name)
	}
	slices.Sort(spelled)
	if !slices.Equal(written, spelled) {
		t.Fatalf("expanded %q, want %q", written, spelled)
	}
	for _, name := range []string{"anims/vismasks.gaf", "units/armfoo.fbi", "sounds/x.wav", "docs/readme.txt"} {
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(name))); !os.IsNotExist(err) {
			t.Fatalf("%s must not be expanded", name)
		}
	}
}

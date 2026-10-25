package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/modlibrary"
)

// installArenaFixtureMod installs a one-file mod carrying the given
// nanolathe-mod.json text into the scratch library XDG_DATA_HOME points at.
func installArenaFixtureMod(t *testing.T, id, metadata string) modlibrary.Mod {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	for name, body := range map[string]string{modlibrary.MetadataFile: metadata, "units/fixture.fbi": "[UNITINFO] { }\n"} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root, err := modlibrary.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	lib, err := modlibrary.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	mod, err := lib.InstallDirectory(dir, modlibrary.InstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return mod
}

// TestResolveArenaModMountsTheInstalledMod locks -mod for the arena
// (docs/DESIGN_MODS_MUTATORS.md §4.3): the installed mod mounts after the
// base root with its own config's content section; a mod without a config is
// plain content with a notice; `none` and no flag mount nothing; and a mod
// that is not installed is refused.
func TestResolveArenaModMountsTheInstalledMod(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	base := t.TempDir()
	mod := installArenaFixtureMod(t, "fixture", `{"schema":2,"id":"fixture","name":"Fixture","version":"2","content":{"layout":{"weapons":"weaponF"}}}`)

	roots, section, notice, err := resolveArenaMod("fixture", base)
	if err != nil || notice != "" {
		t.Fatalf("-mod fixture: %v, notice %q", err, notice)
	}
	if !reflect.DeepEqual(roots, []string{base, mod.Dir}) || section.Name != "fixture" || section.Directories["weapons"] != "weaponF" {
		t.Fatalf("-mod fixture mounted %v with content %+v", roots, section)
	}

	plain := installArenaFixtureMod(t, "plain", `{"schema":1,"id":"plain","name":"Plain","version":"1"}`)
	roots, section, notice, err = resolveArenaMod("plain@1", base)
	if err != nil || !reflect.DeepEqual(roots, []string{base, plain.Dir}) || section.Name != "" || notice == "" {
		t.Fatalf("a configless -mod mounted %v with content %+v, notice %q: %v", roots, section, notice, err)
	}

	for _, selector := range []string{"", "none"} {
		if roots, section, _, err := resolveArenaMod(selector, base); err != nil || roots != nil || section.Name != "" {
			t.Fatalf("-mod %q mounted %v with content %+v: %v", selector, roots, section, err)
		}
	}
	if _, _, _, err := resolveArenaMod("absent", base); err == nil || !strings.Contains(err.Error(), "mod is not installed") {
		t.Fatalf("-mod absent = %v, want the not-installed diagnostic", err)
	}
}

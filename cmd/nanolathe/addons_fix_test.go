package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	contentprofiles "github.com/nanolathe-gg/nanolathe/internal/content/profiles"
	"github.com/nanolathe-gg/nanolathe/internal/modlibrary"
	"github.com/nanolathe-gg/nanolathe/internal/settings"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

// fixPackage writes an add-on package whose payload is a patch-tier revision
// archive, as a hosted fix holds it (§5.7).
func fixPackage(t *testing.T, id, name string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	meta, _ := json.Marshal(modlibrary.Metadata{Schema: 1, ID: id, Name: name, Version: "1"})
	writeModFixtureFile(t, dir, modlibrary.MetadataFile, string(meta))
	writeModFixtureFile(t, dir, "README.txt", "authored fixture\n")
	writeModFixtureFile(t, dir, "rev31.gp3", string(addonArchive(t, files)))
	return dir
}

// The add-on layers mount in the order content, fix, faction, whatever the
// selection's order says; the fix replaces the content add-on it repairs,
// while two fixes stay alternatives (§5.7 "Layers and mounting").
func TestResolveAddonsOrdersLayers(t *testing.T) {
	base, _ := modFixture(t)
	packages := map[string]string{
		"official": addonPackage(t, "official", "Official", map[string]string{"units/aaa.fbi": "[UNITINFO]{UnitName=aaa;Name=Official;}"}),
		"fix":      fixPackage(t, "fix", "Fix", map[string]string{"units/aaa.fbi": "[UNITINFO]{UnitName=aaa;Name=Fixed;}", "units/bbb.fbi": "[UNITINFO]{UnitName=bbb;}"}),
		"fix2":     fixPackage(t, "fix2", "Fix Two", map[string]string{"units/bbb.fbi": "[UNITINFO]{UnitName=bbb;}"}),
		"xeno":     addonPackage(t, "xeno", "Xeno", xenoFiles()),
	}
	for _, id := range []string{"official", "fix", "fix2", "xeno"} {
		if err := runInstallAddon(Options{InstallAddon: packages[id]}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	resolve := func(ids ...string) addonMount {
		t.Helper()
		opts := Options{AddonsSet: true, Addons: []settings.AddonSelection{}}
		for _, id := range ids {
			opts.Addons = append(opts.Addons, settings.AddonSelection{ID: id, Version: "1"})
		}
		out, err := resolveAddons(opts, contentprofiles.Retail(), []string{base}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	// The fix is selected first, yet the mount holds content, fix, faction:
	// the fix may replace the content add-on's shared file, and no note
	// stands between them.
	out := resolve("fix", "official", "xeno")
	var mounted []string
	for _, a := range out.mounted {
		mounted = append(mounted, a.ID)
	}
	if strings.Join(mounted, ",") != "official,fix,xeno" || len(out.notes) != 0 {
		t.Fatalf("mounted %v, notes %q", mounted, out.notes)
	}
	fs := vfs.New()
	defer fs.Close()
	if err := fs.MountGameRoots([]vfs.GameRoot{{Dir: base, Extra: addonExtras(out.mounted), ExtraTier: addonExtraTier(out.factions, out.models)}}); err != nil {
		t.Fatal(err)
	}
	if data, err := fs.ReadFile("units/aaa.fbi"); err != nil || !strings.Contains(string(data), "Fixed") {
		t.Fatalf("units/aaa.fbi = %q, %v, want the fix's copy over the content add-on's", data, err)
	}
	// Two fixes are alternatives: the earlier in the selection keeps its own.
	if out := resolve("fix", "fix2"); len(out.mounted) != 1 || out.mounted[0].ID != "fix" || len(out.notes) != 1 ||
		!strings.Contains(out.notes[0], "Fix Two not mounted: the enabled add-on Fix also supplies units/bbb.fbi") {
		t.Fatalf("two fixes: mounted %v, notes %q", out.mounted, out.notes)
	}
}

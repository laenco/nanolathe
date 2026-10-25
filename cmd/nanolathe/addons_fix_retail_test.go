//go:build retail

package main

import (
	"bytes"
	"os"
	"testing"

	contentprofiles "github.com/nanolathe-gg/nanolathe/internal/content/profiles"
	"github.com/nanolathe-gg/nanolathe/internal/settings"
	"github.com/nanolathe-gg/nanolathe/internal/testsupport"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

// readArchiveFile reads one logical file from an archive.
func readArchiveFile(t *testing.T, archive, path string) []byte {
	t.Helper()
	single := vfs.New()
	defer single.Close()
	if _, err := single.MountArchive(archive, 0); err != nil {
		t.Fatal(err)
	}
	data, err := single.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// A hosted fix installs from its package and mounts over the reference
// install's patch archive: the fixed definition wins over the base and over
// the Cavedog bonus unit it repairs, and the reference install compiles with
// it (§5.7 "Layers and mounting"). The 1.7b6 package is not committed; point
// NANOLATHE_SWITECK_BUGFIX at the hosted zip to run this test.
func TestSwiteckBugfixMountsOverTheBaseRetail(t *testing.T) {
	packagePath := os.Getenv("NANOLATHE_SWITECK_BUGFIX")
	if packagePath == "" {
		t.Skip("set NANOLATHE_SWITECK_BUGFIX to the hosted switecks-bugfix package zip")
	}
	retail := testsupport.RetailRoot(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	var out bytes.Buffer
	if err := runInstallAddon(Options{InstallAddon: packagePath}, &out); err != nil {
		t.Fatal(err)
	}
	fix := settings.AddonSelection{ID: "switecks-bugfix", Version: "1.7b6"}
	resolve := func(root string, addons ...settings.AddonSelection) addonMount {
		t.Helper()
		out.Reset()
		mount, err := resolveAddons(Options{AddonsSet: true, Addons: addons}, contentprofiles.Retail(), []string{root}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return mount
	}
	mount := resolve(retail, fix)
	if len(mount.mounted) != 1 || mount.mounted[0].ID != fix.ID || len(mount.notes) != 0 {
		t.Fatalf("mounted %+v, notes %q", mount.mounted, mount.notes)
	}
	if err := composeAddons(contentprofiles.Retail(), []string{retail}, nil, mount.mounted, mount.factions, mount.models); err != nil {
		t.Fatalf("the reference install does not compile with the fix: %v", err)
	}
	fixed := readArchiveFile(t, mount.mounted[0].Paths[0], "units/armflash.fbi")
	base := vfs.New()
	defer base.Close()
	if err := base.MountGameDirectories([]string{retail}); err != nil {
		t.Fatal(err)
	}
	unfixed, err := base.ReadFile("units/armflash.fbi")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(unfixed, fixed) {
		t.Fatal("the reference install's Flash already equals the fix's")
	}
	composed := vfs.New()
	defer composed.Close()
	if err := composed.MountGameRoots([]vfs.GameRoot{{Dir: retail, Extra: addonExtras(mount.mounted), ExtraTier: addonExtraTier(mount.factions, mount.models)}}); err != nil {
		t.Fatal(err)
	}
	if data, err := composed.ReadFile("units/armflash.fbi"); err != nil || !bytes.Equal(data, fixed) {
		t.Fatal("the fix's Flash did not win over the base install's")
	}

	// The fix repairs the Cavedog bonus units too: with the bonus add-on
	// enabled beside it, the fix mounts after it and its FARK wins (§5.7).
	installBonusAddon(t, retail)
	linked := linkInstall(t, retail, roomBonusUnitArchives...)
	mount = resolve(linked, bonusAddon, fix)
	if len(mount.mounted) != 2 || mount.mounted[0].ID != bonusAddon.ID || mount.mounted[1].ID != fix.ID || len(mount.notes) != 0 {
		t.Fatalf("mounted %+v, notes %q", mount.mounted, mount.notes)
	}
	fixedFark := readArchiveFile(t, mount.mounted[1].Paths[0], "units/armfark.fbi")
	bonusFark := readArchiveFile(t, mount.mounted[0].Paths[0], "units/armfark.fbi")
	if bytes.Equal(bonusFark, fixedFark) {
		t.Fatal("the Cavedog bonus FARK already equals the fix's")
	}
	layered := vfs.New()
	defer layered.Close()
	if err := layered.MountGameRoots([]vfs.GameRoot{{Dir: linked, Extra: addonExtras(mount.mounted), ExtraTier: addonExtraTier(mount.factions, mount.models)}}); err != nil {
		t.Fatal(err)
	}
	if data, err := layered.ReadFile("units/armfark.fbi"); err != nil || !bytes.Equal(data, fixedFark) {
		t.Fatal("the fix's FARK did not win over the Cavedog bonus archive's")
	}
}

// map-expand expands one HPI-family map archive — the .ufo inside a community
// map ZIP — into a package root that tools/package-maps accepts. It never
// rewrites bytes, and it drops everything a map package may not carry
// (docs/DESIGN_MODS_MUTATORS.md §5.6). Expansion is a packaging step: the
// engine reads the original archive in the game folder; this tool exists so
// the curated catalogue can be built from authored archives as received.
//
// Usage: map-expand --archive File.ufo --out ROOT [--verbose]
package main

import (
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nanolathe-gg/nanolathe/vfs"
)

func main() {
	archive := flag.String("archive", "", "HPI-family archive to expand (.ufo, .hpi, .ccx, .gp3, .swx)")
	out := flag.String("out", "", "package root directory to write")
	verbose := flag.Bool("verbose", false, "list every skipped entry")
	flag.Parse()
	if *archive == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "map-expand: --archive and --out are required")
		os.Exit(2)
	}
	if err := run(*archive, *out, *verbose); err != nil {
		fmt.Fprintln(os.Stderr, "map-expand:", err)
		os.Exit(1)
	}
}

// allowed mirrors tools/package-maps' map-package file rule: the map pair,
// its feature definitions and the art families a map may ship, plus one
// root-level credits/readme document.
func allowed(name string) bool {
	parts := strings.Split(strings.ToLower(strings.Trim(name, "/")), "/")
	suffix := strings.ToLower(path.Ext(parts[len(parts)-1]))
	if len(parts) == 1 {
		stem := strings.ToLower(parts[0])
		return suffix == ".txt" || suffix == ".md" || suffix == ".rtf" || suffix == ".html" ||
			strings.HasPrefix(stem, "readme") || strings.HasPrefix(stem, "license") || stem == "copying"
	}
	switch parts[0] {
	case "maps":
		return len(parts) == 2 && (suffix == ".ota" || suffix == ".tnt")
	case "features":
		// Feature definitions nest by group in the community packs
		// (features/<group>/<file>.tdf), as tools/package-maps also allows.
		return suffix == ".tdf"
	case "anims":
		return len(parts) == 2 && suffix == ".gaf" && strings.ToLower(name) != "anims/vismasks.gaf"
	case "objects3d":
		return len(parts) == 2 && suffix == ".3do"
	case "textures":
		return len(parts) == 2 && (suffix == ".gaf" || suffix == ".bmp" || suffix == ".pcx")
	}
	return false
}

func run(archive, out string, verbose bool) error {
	fs := vfs.New()
	defer fs.Close()
	if _, err := fs.MountArchive(archive, 0); err != nil {
		return err
	}
	var files []vfs.EntryInfo
	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := fs.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir {
				if err := walk(entry.Path); err != nil {
					return err
				}
				continue
			}
			files = append(files, entry)
		}
		return nil
	}
	if err := walk(""); err != nil {
		return err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	seen := map[string]string{}
	var written, skipped int
	for _, entry := range files {
		name := strings.TrimPrefix(entry.Path, "/")
		if !allowed(name) {
			if verbose {
				fmt.Println("skip", name)
			}
			skipped++
			continue
		}
		// Logical paths are folded for VFS lookup; localize the authored path
		// separately so output keeps its casing without escaping the root.
		name = strings.ReplaceAll(entry.OriginalPath, "\\", "/")
		relative, err := filepath.Localize(name)
		if err != nil {
			return fmt.Errorf("%s: invalid output path: %w", name, err)
		}
		folded := strings.ToLower(name)
		if other, dup := seen[folded]; dup {
			return fmt.Errorf("case-insensitive duplicate: %s and %s", other, name)
		}
		seen[folded] = name
		data, err := fs.ReadFileLimit(entry.Path, 64<<20)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		target := filepath.Join(out, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
		written++
	}
	fmt.Printf("map-expand: %d files written, %d skipped\n", written, skipped)
	return nil
}

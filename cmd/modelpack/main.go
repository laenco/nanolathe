// Command modelpack assembles a pinned, model-only add-on without rewriting
// its source assets (docs/DESIGN_MODS_MUTATORS.md §5.7).
//
//	modelpack -recipe modelconfigs/evolva-models-1.json \
//	  -source-dir ~/Downloads -out /tmp/modelpacks
package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type recipe struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Summary  string   `json:"summary"`
	Homepage string   `json:"homepage,omitempty"`
	Archive  string   `json:"archive"`
	Readme   []string `json:"readme"`
	// Sources are priority ordered. A later source fills only missing names.
	Sources []source `json:"sources"`
}

type source struct {
	File   string `json:"file"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func main() {
	recipePath := flag.String("recipe", "", "model-pack recipe JSON")
	sourceDir := flag.String("source-dir", "", "directory holding the pinned source ZIPs")
	out := flag.String("out", "", "output directory; writes <id>-<version>/ and <id>-<version>.zip")
	flag.Parse()
	if *recipePath == "" || *sourceDir == "" || *out == "" || flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*recipePath, *sourceDir, *out, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "nanolathe: model pack failed: logical path %s, providers searched [%s], expected reproducible model-only add-on: %v\n", *recipePath, *sourceDir, err)
		os.Exit(1)
	}
}

func run(recipePath, sourceDir, out string, diagnostic io.Writer) error {
	r, err := readRecipe(recipePath)
	if err != nil {
		return err
	}
	models, counts, err := collect(r, sourceDir)
	if err != nil {
		return err
	}
	contents, err := packageContents(r, models)
	if err != nil {
		return err
	}
	if err := writePackage(r, contents, out); err != nil {
		return err
	}
	for i, src := range r.Sources {
		fmt.Fprintf(diagnostic, "%s: selected %d models\n", src.File, counts[i])
	}
	fmt.Fprintf(diagnostic, "%s: %d models; %s %d bytes SHA-256 %s\n", filepath.Join(out, r.ID+"-"+r.Version), len(models), r.Archive, len(contents[r.Archive]), digest(contents[r.Archive]))
	return nil
}

func readRecipe(name string) (recipe, error) {
	var r recipe
	data, err := os.ReadFile(name)
	if err != nil {
		return r, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return r, fmt.Errorf("%s: %w", name, err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return r, fmt.Errorf("%s: expected exactly one recipe JSON value", name)
	}
	if !identifier(r.ID) || !identifier(r.Version) || strings.TrimSpace(r.Name) == "" || len(r.Readme) == 0 || len(r.Sources) == 0 {
		return r, fmt.Errorf("%s: lowercase id and version, name, readme and sources are required", name)
	}
	if !baseName(r.Archive) || !strings.EqualFold(filepath.Ext(r.Archive), ".ufo") {
		return r, fmt.Errorf("%s: archive must be a root .ufo filename", name)
	}
	seen := map[string]bool{}
	for _, src := range r.Sources {
		key := foldASCII(src.File)
		if !baseName(src.File) || !strings.EqualFold(filepath.Ext(src.File), ".zip") || seen[key] {
			return r, fmt.Errorf("%s: source ZIP filenames must be unique root names: %q", name, src.File)
		}
		seen[key] = true
		hash, err := hex.DecodeString(src.SHA256)
		if src.Size <= 0 || err != nil || len(hash) != 32 || src.SHA256 != strings.ToLower(src.SHA256) {
			return r, fmt.Errorf("%s: source %s requires a positive byte size and lowercase SHA-256", name, src.File)
		}
	}
	return r, nil
}

func identifier(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func baseName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\:\x00\r\n\t") && strings.TrimSpace(s) == s
}

// VFS logical names fold ASCII only; source member spelling remains in the
// provenance table [02 §2].
func foldASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

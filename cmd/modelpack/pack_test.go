package main

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nanolathe-gg/nanolathe/formats"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

type member struct {
	name string
	data []byte
	mode os.FileMode
}

func authoredModel(t *testing.T, name string) []byte {
	t.Helper()
	m := &formats.ThreeDO{Root: 0, Objects: []formats.ThreeDOObject{{
		Name: name, Selection: -1, Parent: -1, FirstChild: -1, NextSibling: -1,
		Vertices:   []formats.ThreeDOVertex{{}, {X: 65536}, {X: 65536, Z: 65536}, {Z: 65536}},
		Primitives: []formats.ThreeDOPrimitive{{VertexIndices: []uint16{0, 1, 2, 3}, ColorIndex: 8}},
	}}}
	data, err := formats.EncodeThreeDO(m)
	if err != nil {
		t.Fatal(err)
	}
	// Authored trailing bytes are deliberately outside the parsed structure;
	// preserving them catches an accidental decode/encode copy path.
	return append(data, []byte("unchanged trailer: "+name)...)
}

func authoredZIP(t *testing.T, members ...member) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, m := range members {
		hdr := &zip.FileHeader{Name: m.name, Method: zip.Store}
		if m.mode != 0 {
			hdr.SetMode(m.mode)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(m.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func pin(t *testing.T, dir, name string, data []byte) source {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return source{File: name, Size: int64(len(data)), SHA256: digest(data)}
}

func testRecipe(sources ...source) recipe {
	return recipe{ID: "authored-models", Name: "Authored models", Version: "1", Archive: "Models.ufo", Readme: []string{"Authored fixture."}, Sources: sources}
}

func TestFirstSourceWinsAndLaterSourcesFillGaps(t *testing.T) {
	dir := t.TempDir()
	first, replacement, gap := authoredModel(t, "first"), authoredModel(t, "replacement"), authoredModel(t, "gap")
	s1 := pin(t, dir, "first.zip", authoredZIP(t, member{name: "Objects3D/", mode: os.ModeDir | 0o755}, member{name: "Objects3D/ARM.3DO", data: first}))
	s2 := pin(t, dir, "later.zip", authoredZIP(t, member{name: "objects3d/arm.3do", data: replacement}, member{name: "objects3d/GAP.3do", data: gap}))
	models, counts, err := collect(testRecipe(s1, s2), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(counts, []int{1, 1}) {
		t.Fatalf("selected source counts %v", counts)
	}
	if len(models) != 2 || models[0].Path != "objects3d/arm.3do" || models[1].Path != "objects3d/gap.3do" {
		t.Fatalf("models %+v", models)
	}
	if !bytes.Equal(models[0].Data, first) || models[0].Source != s1 || models[0].Member != "Objects3D/ARM.3DO" || !bytes.Equal(models[1].Data, gap) || models[1].Source != s2 {
		t.Fatal("source precedence, provenance or model bytes changed")
	}
}

func TestSourcePinsRefuseWrongSizeAndHash(t *testing.T) {
	dir := t.TempDir()
	src := pin(t, dir, "source.zip", authoredZIP(t, member{name: "Objects3D/arm.3do", data: authoredModel(t, "base")}))
	for _, test := range []struct {
		name string
		pin  source
	}{
		{"size", source{File: src.File, Size: src.Size + 1, SHA256: src.SHA256}},
		{"sha256", source{File: src.File, Size: src.Size, SHA256: strings.Repeat("0", 64)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := collect(testRecipe(test.pin), dir); err == nil || !strings.Contains(err.Error(), "source pin mismatch") {
				t.Fatalf("got %v, expected pin refusal", err)
			}
		})
	}
}

func TestSourcesRefuseDuplicatesForeignPathsAndMalformedModels(t *testing.T) {
	valid := authoredModel(t, "valid")
	badSelection := bytes.Clone(valid)
	binary.LittleEndian.PutUint32(badSelection[12:16], 99)
	for _, test := range []struct {
		name    string
		members []member
		message string
	}{
		{"case duplicate", []member{{name: "Objects3D/arm.3do", data: valid}, {name: "objects3d/ARM.3DO", data: valid}}, "duplicate"},
		{"parent traversal", []member{{name: "objects3d/../arm.3do", data: valid}}, "unsafe"},
		{"absolute", []member{{name: "/objects3d/arm.3do", data: valid}}, "unsafe"},
		{"windows drive", []member{{name: `C:\objects3d\arm.3do`, data: valid}}, "unsafe"},
		{"script", []member{{name: "scripts/arm.cob", data: valid}}, "foreign"},
		{"wrong extension", []member{{name: "objects3d/arm.fbi", data: valid}}, "foreign"},
		{"nested directory", []member{{name: "objects3d/nested/arm.3do", data: valid}}, "foreign"},
		{"symlink", []member{{name: "objects3d/arm.3do", data: valid, mode: os.ModeSymlink | 0o777}}, "regular"},
		{"truncated", []member{{name: "objects3d/arm.3do", data: []byte{1}}}, "invalid 3DO"},
		{"compiler rejection", []member{{name: "objects3d/arm.3do", data: badSelection}}, "selection primitive"},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := authoredZIP(t, test.members...)
			if _, err := sourceModels(source{File: "fixture.zip"}, data); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("got %v, expected %s refusal", err, test.message)
			}
		})
	}
	// Validation also covers files shadowed by an earlier source.
	dir := t.TempDir()
	first := pin(t, dir, "first.zip", authoredZIP(t, member{name: "objects3d/arm.3do", data: valid}))
	bad := pin(t, dir, "later.zip", authoredZIP(t, member{name: "objects3d/arm.3do", data: badSelection}))
	if _, _, err := collect(testRecipe(first, bad), dir); err == nil {
		t.Fatal("a malformed shadowed model must still refuse its source")
	}
}

func TestPackageIsDeterministicModelOnlyAndBytePreserving(t *testing.T) {
	sourceDir := t.TempDir()
	data := authoredModel(t, "original")
	src := pin(t, sourceDir, "fixture.zip", authoredZIP(t, member{name: "Objects3D/Original.3DO", data: data}))
	r := testRecipe(src)
	r.Homepage, r.Summary = "https://example.invalid/models", "Authored replacement geometry."
	recipeData, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	recipePath := filepath.Join(sourceDir, "recipe.json")
	if err := os.WriteFile(recipePath, recipeData, 0o644); err != nil {
		t.Fatal(err)
	}
	outputs := []string{t.TempDir(), t.TempDir()}
	for _, out := range outputs {
		if err := run(recipePath, sourceDir, out, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	name := r.ID + "-" + r.Version
	zipped := make([][]byte, len(outputs))
	for i, out := range outputs {
		zipped[i], err = os.ReadFile(filepath.Join(out, name+".zip"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(zipped[0], zipped[1]) {
		t.Fatal("identical sources and recipe produced different packages")
	}
	zr, err := zip.NewReader(bytes.NewReader(zipped[0]), int64(len(zipped[0])))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	contents := map[string][]byte{}
	for _, f := range zr.File {
		names = append(names, f.Name)
		if !f.Modified.Equal(time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)) || f.Mode() != 0o644 || f.Method != zip.Store {
			t.Fatalf("unstable header %+v", f.FileHeader)
		}
		stream, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		contents[f.Name], err = io.ReadAll(stream)
		stream.Close()
		if err != nil {
			t.Fatal(err)
		}
		loose, err := os.ReadFile(filepath.Join(outputs[0], name, f.Name))
		if err != nil || !bytes.Equal(loose, contents[f.Name]) {
			t.Fatalf("loose package differs for %s: %v", f.Name, err)
		}
	}
	if !reflect.DeepEqual(names, []string{"MODEL-SOURCES.txt", "Models.ufo", "README.txt", "nanolathe-mod.json"}) {
		t.Fatalf("root members %q", names)
	}
	var meta map[string]any
	if err := json.Unmarshal(contents["nanolathe-mod.json"], &meta); err != nil {
		t.Fatal(err)
	}
	wantMeta := map[string]any{"schema": float64(1), "id": r.ID, "name": r.Name, "version": r.Version, "summary": r.Summary, "homepage": r.Homepage}
	if !reflect.DeepEqual(meta, wantMeta) {
		t.Fatalf("metadata %+v", meta)
	}
	wantAudit := "objects3d/original.3do\tfixture.zip\t" + src.SHA256 + "\tObjects3D/Original.3DO\t" + digest(data) + "\n"
	if !strings.Contains(string(contents["MODEL-SOURCES.txt"]), wantAudit) {
		t.Fatal("model audit lacks exact source and model identity")
	}
	fs := vfs.New()
	defer fs.Close()
	archive := contents[r.Archive]
	if _, err := fs.MountArchiveReader(r.Archive, bytes.NewReader(archive), int64(len(archive)), 10, vfs.ArchiveOptions{VerifyChecksums: true}); err != nil {
		t.Fatal(err)
	}
	root, err := fs.ReadDir("")
	if err != nil || len(root) != 1 || root[0].Path != "objects3d" || !root[0].IsDir {
		t.Fatalf("archive root %+v, %v", root, err)
	}
	models, err := fs.ReadDir("objects3d")
	if err != nil || len(models) != 1 || models[0].Path != "objects3d/original.3do" || models[0].IsDir {
		t.Fatalf("archive models %+v, %v", models, err)
	}
	got, err := fs.ReadFile("objects3d/original.3do")
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("model bytes changed: %v", err)
	}
	if err := run(recipePath, sourceDir, outputs[0], io.Discard); err == nil {
		t.Fatal("existing output must not be overwritten")
	}
}

func TestRecipeRefusesEscapingSourceAndGameplayFields(t *testing.T) {
	src := source{File: "fixture.zip", Size: 1, SHA256: strings.Repeat("0", 64)}
	for _, test := range []struct {
		name   string
		modify func(*recipe)
		extra  string
	}{
		{"source traversal", func(r *recipe) { r.Sources[0].File = "../fixture.zip" }, ""},
		{"archive traversal", func(r *recipe) { r.Archive = "../Models.ufo" }, ""},
		{"version traversal", func(r *recipe) { r.Version = "../1" }, ""},
		{"unknown gameplay", func(r *recipe) {}, `,"gameplay":{"mode":"modern"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := testRecipe(src)
			test.modify(&r)
			data, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if test.extra != "" {
				data = append(data[:len(data)-1], []byte(test.extra+"}")...)
			}
			name := filepath.Join(t.TempDir(), "recipe.json")
			if err := os.WriteFile(name, data, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := readRecipe(name); err == nil {
				t.Fatal("invalid recipe accepted")
			}
		})
	}
}

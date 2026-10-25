package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nanolathe-gg/nanolathe/formats"
	"github.com/nanolathe-gg/nanolathe/internal/model"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

type selectedModel struct {
	Path, Member string
	Source       source
	Data         []byte
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func collect(r recipe, sourceDir string) ([]selectedModel, []int, error) {
	selected := map[string]selectedModel{}
	counts := make([]int, len(r.Sources))
	for i, src := range r.Sources {
		data, err := os.ReadFile(filepath.Join(sourceDir, src.File))
		if err != nil {
			return nil, nil, err
		}
		if int64(len(data)) != src.Size || digest(data) != src.SHA256 {
			return nil, nil, fmt.Errorf("%s: source pin mismatch: got size %d SHA-256 %s, expected size %d SHA-256 %s", src.File, len(data), digest(data), src.Size, src.SHA256)
		}
		models, err := sourceModels(src, data)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", src.File, err)
		}
		for _, m := range models {
			if _, present := selected[m.Path]; !present {
				selected[m.Path] = m
				counts[i]++
			}
		}
	}
	paths := make([]string, 0, len(selected))
	for p := range selected {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	models := make([]selectedModel, 0, len(paths))
	for _, p := range paths {
		models = append(models, selected[p])
	}
	return models, counts, nil
}

func sourceModels(src source, data []byte) ([]selectedModel, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var models []selectedModel
	for _, f := range zr.File {
		logical, directory, err := modelPath(f.Name)
		if err != nil {
			return nil, err
		}
		if seen[logical] {
			return nil, fmt.Errorf("duplicate case-insensitive source path %q", f.Name)
		}
		seen[logical] = true
		if f.Mode()&os.ModeType != 0 && !f.Mode().IsDir() {
			return nil, fmt.Errorf("%q: expected regular model file or directory", f.Name)
		}
		if directory {
			if !f.FileInfo().IsDir() || f.UncompressedSize64 != 0 {
				return nil, fmt.Errorf("%q: expected empty directory entry", f.Name)
			}
			continue
		}
		if f.FileInfo().IsDir() {
			return nil, fmt.Errorf("%q: expected regular model file", f.Name)
		}
		stream, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name, err)
		}
		modelData, readErr := io.ReadAll(stream)
		closeErr := stream.Close()
		if readErr != nil {
			return nil, fmt.Errorf("%s: %w", f.Name, readErr)
		}
		if closeErr != nil {
			return nil, closeErr
		}
		// Decode and compile validation check the engine's host-safety policy
		// [fmt 3do "Host decoder safety policy"]. The output keeps modelData,
		// never an encoder's reconstructed representation.
		three, err := formats.LoadThreeDO(modelData)
		if err == nil {
			err = model.ValidateSource(three)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: invalid 3DO: %w", f.Name, err)
		}
		models = append(models, selectedModel{Path: logical, Member: f.Name, Source: src, Data: modelData})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("source contains no objects3d/*.3do models")
	}
	return models, nil
}

func modelPath(member string) (logical string, directory bool, err error) {
	// Do not clean away traversal components or an absolute prefix. Sources
	// may spell the one directory and filenames in either ASCII case.
	normal := strings.ReplaceAll(member, `\`, "/")
	parts := strings.Split(normal, "/")
	if len(parts) != 2 || foldASCII(parts[0]) != "objects3d" || strings.ContainsAny(member, ":\x00\r\n\t") {
		return "", false, fmt.Errorf("foreign or unsafe source path %q: expected objects3d/*.3do", member)
	}
	if parts[1] == "" {
		return "objects3d/", true, nil
	}
	if !baseName(parts[1]) || !strings.EqualFold(filepath.Ext(parts[1]), ".3do") || len(parts[1]) == len(".3do") {
		return "", false, fmt.Errorf("foreign or unsafe source path %q: expected objects3d/*.3do", member)
	}
	return "objects3d/" + foldASCII(parts[1]), false, nil
}

func packageContents(r recipe, models []selectedModel) (map[string][]byte, error) {
	files := make([]vfs.ArchiveFile, 0, len(models))
	var audit strings.Builder
	audit.WriteString("logical_path\tsource_zip\tsource_sha256\tsource_member\tmodel_sha256\n")
	for _, m := range models {
		files = append(files, vfs.ArchiveFile{Path: m.Path, Data: m.Data})
		fmt.Fprintf(&audit, "%s\t%s\t%s\t%s\t%s\n", m.Path, m.Source.File, m.Source.SHA256, m.Member, digest(m.Data))
	}
	var archive bytes.Buffer
	if err := vfs.WriteArchive(&archive, files, vfs.ArchiveWriteOptions{Compress: true}); err != nil {
		return nil, err
	}
	// Identity metadata carries no content profile or gameplay configuration.
	meta := struct {
		Schema   int    `json:"schema"`
		ID       string `json:"id"`
		Name     string `json:"name"`
		Version  string `json:"version"`
		Summary  string `json:"summary,omitempty"`
		Homepage string `json:"homepage,omitempty"`
	}{1, r.ID, r.Name, r.Version, r.Summary, r.Homepage}
	metadata, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return nil, err
	}
	var readme strings.Builder
	fmt.Fprintf(&readme, "%s - Nanolathe compilation %s\n\n%s\n", r.Name, r.Version, strings.Join(r.Readme, "\n\n"))
	if r.Homepage != "" {
		fmt.Fprintf(&readme, "\nOriginal project homepage: %s\n", r.Homepage)
	}
	readme.WriteString("\nSource ZIPs, in first-source-wins priority order:\n")
	for _, src := range r.Sources {
		fmt.Fprintf(&readme, "%s  %d bytes  SHA-256 %s\n", src.File, src.Size, src.SHA256)
	}
	readme.WriteString("\nMODEL-SOURCES.txt records each retained model's source member, source ZIP SHA-256 and unchanged model SHA-256.\n")
	return map[string][]byte{
		r.Archive:            archive.Bytes(),
		"nanolathe-mod.json": append(metadata, '\n'),
		"README.txt":         []byte(readme.String()),
		"MODEL-SOURCES.txt":  []byte(audit.String()),
	}, nil
}

func writePackage(r recipe, contents map[string][]byte, out string) error {
	name := r.ID + "-" + r.Version
	dir, zipPath := filepath.Join(out, name), filepath.Join(out, name+".zip")
	for _, target := range []string{dir, zipPath} {
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			if err != nil {
				return err
			}
			return fmt.Errorf("%s exists; choose an empty output directory", target)
		}
	}
	paths := make([]string, 0, len(contents))
	for p := range contents {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var packed bytes.Buffer
	zw := zip.NewWriter(&packed)
	for _, p := range paths {
		hdr := &zip.FileHeader{Name: p, Method: zip.Store, Modified: time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)}
		hdr.SetMode(0o644)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		if _, err := w.Write(contents[p]); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	for _, p := range paths {
		if err := os.WriteFile(filepath.Join(dir, p), contents[p], 0o644); err != nil {
			return err
		}
	}
	zf, err := os.OpenFile(zipPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = zf.Write(packed.Bytes())
	closeErr := zf.Close()
	if err != nil {
		return err
	}
	return closeErr
}

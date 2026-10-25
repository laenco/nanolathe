package main

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/nanolathe-gg/nanolathe/internal/version"
)

// Startup diagnostics are host observations (DESIGN_CONTENT_VFS §4). Write
// them before loading so even a failed start carries useful issue context.
// Standard output belongs to command results, JSON reports and film streams.
func writeStartupSystemReport(w io.Writer) {
	fmt.Fprintf(w, "nanolathe: startup: %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(w, "nanolathe: system: os=%s arch=%s logical-cpus=%d gomaxprocs=%d go=%s\n",
		runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.GOMAXPROCS(0), runtime.Version())
	info, _ := debug.ReadBuildInfo()
	revision, modified, engine := buildRevision(info)
	fmt.Fprintf(w, "nanolathe: build: profile=%s revision=%s modified=%s ebitengine=%s\n",
		version.ProfileID(), revision, modified, engine)
	fmt.Fprintf(w, "nanolathe: build manifest: %s\n", startupBuildManifest(version.CurrentBuildManifest()))
}

// buildRevision is the source revision a build records, its modified flag and
// its Ebitengine version. Source installers build an archive without VCS
// metadata and place the actual source revision beside the executable
// (tools/installer/README.md).
func buildRevision(info *debug.BuildInfo) (revision, modified, engine string) {
	revision, modified, engine = startupBuildIdentity(info)
	if revision == "unavailable" {
		if exe, err := os.Executable(); err == nil {
			if installed := startupInstalledRevision(filepath.Dir(exe)); installed != "" {
				revision = installed
			}
		}
	}
	return revision, modified, engine
}

// buildRevisionLabel is the short revision the Nanolathe screen shows beside
// its wordmark, marked when the build's tree held uncommitted changes; a
// build that records no revision is a development build.
var buildRevisionLabel = sync.OnceValue(func() string {
	info, _ := debug.ReadBuildInfo()
	revision, modified, _ := buildRevision(info)
	if len(revision) < 7 || revision == "unavailable" {
		return "dev build"
	}
	if modified == "true" {
		return revision[:7] + " modified"
	}
	return revision[:7]
})

// startupBuildManifest names the common build manifest the binary carries
// (docs/DESIGN_MULTIPLAYER.md §8.7): its digest, which is the build players
// must share to play together, or why there is none. An installer build is
// stamped; a development build is not.
func startupBuildManifest(m version.BuildManifest, err error) string {
	if err != nil {
		return "refused (" + err.Error() + ")"
	}
	if !m.Stamped() {
		return "unstamped (development build)"
	}
	digest, err := m.Digest()
	if err != nil {
		return "refused (" + err.Error() + ")"
	}
	return hex.EncodeToString(digest[:])
}

func startupBuildIdentity(info *debug.BuildInfo) (revision, modified, engine string) {
	revision, modified, engine = "unavailable", "unknown", "unavailable"
	if info == nil {
		return
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value
		}
	}
	for _, dep := range info.Deps {
		if dep.Path == "github.com/hajimehoshi/ebiten/v2" {
			engine = dep.Version
			if dep.Replace != nil {
				engine += " (replaced)"
			}
		}
	}
	return
}

func startupInstalledRevision(directory string) string {
	if f, err := os.Open(filepath.Join(directory, "source-revision")); err == nil {
		data, readErr := io.ReadAll(io.LimitReader(f, 128))
		_ = f.Close()
		revision := strings.TrimSpace(string(data))
		if readErr == nil && len(revision) == 40 {
			if _, err := hex.DecodeString(revision); err == nil {
				return revision + " (installed source)"
			}
		}
	}
	// Older installers only recorded the pinned source in their release manifest.
	if f, err := os.Open(filepath.Join(directory, "release.txt")); err == nil {
		defer f.Close()
		if revision := installedSourceRevision(f); revision != "" {
			return revision + " (release manifest)"
		}
	}
	return ""
}

func installedSourceRevision(r io.Reader) string {
	scanner := bufio.NewScanner(io.LimitReader(r, 64<<10))
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if !ok || key != "source_revision" || len(value) != 40 {
			continue
		}
		if _, err := hex.DecodeString(value); err == nil {
			return value
		}
	}
	return ""
}

func writeContentStartupReport(w io.Writer, cs *contentSet, elapsed time.Duration) {
	if cs == nil || cs.unmappedMount == nil || cs.startupReported {
		return
	}
	cs.startupReported = true
	providers := cs.unmappedMount.Providers()
	archives, directories, files := 0, 0, 0
	names := make([]string, 0, len(providers))
	for _, provider := range providers {
		name := filepath.Base(provider.ID)
		if provider.Type == "directory" {
			directories++
			name = fmt.Sprintf("loose root (mount %d)", provider.MountOrder)
		} else {
			archives++
		}
		files += provider.Files
		names = append(names, fmt.Sprintf("%s (%d files)", name, provider.Files))
	}
	fmt.Fprintf(w, "nanolathe: content: profile=%s mod=%s archives=%d loose-roots=%d indexed-file-entries=%d load=%s\n",
		cs.profile, cs.modSelector(), archives, directories, files, elapsed.Round(time.Millisecond))
	fmt.Fprintf(w, "nanolathe: content providers (highest precedence first): %s\n", strings.Join(names, ", "))
	for _, note := range cs.unmappedMount.Notes() {
		fmt.Fprintf(w, "nanolathe: content mount: %s\n", strings.TrimPrefix(note, "nanolathe: "))
	}
}

func writeWindowStartupReport(w io.Writer, shell *gameShell, elapsed time.Duration) {
	if shell == nil {
		return
	}
	if shell.cs != nil {
		writeContentStartupReport(w, shell.cs, shell.cs.loadDuration)
	}
	writeFrontendStartupReport(w, shell)
	entry := "menus"
	if shell.opts.Map != "" {
		entry = "map " + fmt.Sprintf("%q", shell.opts.Map)
	} else if shell.opts.LoadSave != "" {
		entry = "saved battle"
		if shell.battle == nil {
			// A save may open a campaign briefing or queue a mod switch.
			entry = "save entry"
		}
	} else if shell.battle != nil {
		entry = "battle"
	}
	limit, _ := shell.effectiveUnitLimit()
	if shell.battle != nil && shell.battle.sess != nil {
		limit = shell.battle.sess.Skirmish.UnitLimit
	}
	fmt.Fprintf(w, "nanolathe: settings: gameplay=%s renderer=%s fps-cap=%d fullscreen=%t unit-limit=%d\n",
		shell.gameplay.Normalize(), shell.presentation.Renderer, shell.presentation.FPS, shell.fullscreen, limit)
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	fmt.Fprintf(w, "nanolathe: window ready: entry=%s load=%s go-heap-bytes=%d\n", entry, elapsed.Round(time.Millisecond), mem.HeapAlloc)
}

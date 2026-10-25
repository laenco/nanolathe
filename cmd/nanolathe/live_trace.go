package main

import (
	"fmt"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/nanolathe-gg/nanolathe/internal/camera"
	"github.com/nanolathe-gg/nanolathe/internal/headless"
	"github.com/nanolathe-gg/nanolathe/internal/platform/ebitenapp"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/visibility"
)

// The live window trace (docs/BATTLE_BENCHMARK.md "Live window trace") times
// the ordinary window loop — Ebitengine's own scheduling, the present cap, the
// record/submit pipeline and the asynchronous simulation — on a direct --map
// battle, optionally staged at a scale an ordinary opening never reaches, or
// on the player's own games started from the menus. It is a host diagnostic:
// the battle it stages is the benchmark's fixture, and the trace itself never
// reaches the client or the session.

// liveScene is a parsed --live-scene: one of
//
//	coastal[:scale]         the battle benchmark's coastal scene, rosters scaled
//	field[:army]            the simulation benchmark's three armies on --map
//	capture:<dir>[:copies]  a Ctrl+Shift+F11 bundle, staged copies times
type liveScene struct {
	Kind    string  `json:"kind"`
	Scale   float64 `json:"scale,omitempty"`
	Army    int     `json:"army,omitempty"`
	Capture string  `json:"capture,omitempty"`
	Copies  int     `json:"copies,omitempty"`
}

func parseLiveScene(text string) (liveScene, error) {
	if text == "" {
		return liveScene{}, nil
	}
	kind, rest, _ := strings.Cut(text, ":")
	scene := liveScene{Kind: kind}
	bad := func() (liveScene, error) {
		return liveScene{}, fmt.Errorf("nanolathe: invalid live scene: logical path %s, providers searched [live-scene], expected coastal[:scale], field[:army] or capture:<dir>[:copies]", text)
	}
	switch kind {
	case "coastal":
		scene.Scale = 1
		if rest != "" {
			v, err := strconv.ParseFloat(rest, 64)
			if err != nil || v <= 0 || v > 8 {
				return bad()
			}
			scene.Scale = v
		}
	case "field":
		scene.Army = headless.SimBenchDefaultArmySize
		if rest != "" {
			v, err := strconv.Atoi(rest)
			if err != nil || v < headless.SimBenchMinArmySize || v > headless.SimBenchMaxArmySize {
				return bad()
			}
			scene.Army = v
		}
	case "capture":
		// A Windows path opens with a volume, C:, whose colon is not the
		// copies separator; elsewhere the volume is empty.
		volume := filepath.VolumeName(rest)
		dir, copies, _ := strings.Cut(rest[len(volume):], ":")
		dir = volume + dir
		if dir == "" {
			return bad()
		}
		scene.Capture, scene.Copies = dir, 1
		if copies != "" {
			v, err := strconv.Atoi(copies)
			if err != nil || v < 1 || v > 4 {
				return bad()
			}
			scene.Copies = v
		}
	default:
		return bad()
	}
	return scene, nil
}

// composeLiveFieldBattle composes the simulation benchmark's scene in place of
// the direct --map skirmish: three computer armies of the requested size and
// a passive human placeholder that is the viewing slot.
func composeLiveFieldBattle(opts Options, cs *contentSet, scene liveScene) (headless.FreshBattle, error) {
	seed := uint32(7)
	if opts.Seed >= 0 {
		seed = uint32(opts.Seed)
	}
	limit := max(headless.SimBenchDefaultUnitLimit, scene.Army+1)
	composed, field, err := headless.ComposeSimBenchBattle(headless.SimBenchOptions{
		Map: opts.Map, ArmySize: scene.Army, UnitLimit: limit, Gameplay: opts.Gameplay, Seed: seed, Difficulty: 1,
	}, cs.fs, nil)
	if err == nil {
		liveFieldCentre = [2]int32{field.CentreX, field.CentreZ}
	}
	return composed, err
}

// liveFieldCentre is where the field scene's armies converge.
var liveFieldCentre [2]int32

// stageLiveScene stages the coastal or capture scene into the entered battle
// and points the camera at it. The field scene was composed with the battle;
// it only needs the camera and the unmasked view, since its viewing slot owns
// nothing but a distant commander.
func stageLiveScene(opts Options, shell *gameShell, scene liveScene) (map[string]any, error) {
	b := shell.battle
	if b == nil || b.sess == nil {
		return nil, fmt.Errorf("nanolathe: live scene: logical path %s, providers searched [battle], expected an entered battle", opts.Map)
	}
	s := b.sess
	meta := map[string]any{"scene": scene}
	switch scene.Kind {
	case "coastal":
		staged := opts
		staged.BenchmarkScale = scene.Scale
		coastal, err := stageCoastalBenchmark(staged, s)
		if err != nil {
			return nil, err
		}
		b.cam.JumpToBattleViewCenter(coastal.X, coastal.Z)
		meta["coastal_scene"] = coastal
	case "capture":
		staged := opts
		staged.BenchmarkCapture = scene.Capture
		staged.BenchmarkCaptureCopies = scene.Copies
		staged.BenchmarkFactories = true
		staged.ShotSize = fmt.Sprintf("%dx%d", shell.display.Width, shell.display.Height)
		capture, err := stageCaptureBenchmark(staged, s)
		if err != nil {
			return nil, err
		}
		b.cam.JumpTo(capture.CameraX, capture.CameraZ)
		meta["capture_scene"] = capture
	case "field":
		b.cam.JumpToBattleViewCenter(liveFieldCentre[0], liveFieldCentre[1])
		// Unmask the map for the viewing slot, as +nowisee does, so the
		// three armies are drawn rather than fogged.
		_ = s.EnqueueHumanCommand(session.HumanCommand{Kind: session.HumanVisibility, Visibility: session.HumanVisibilityCommand{ClearMask: visibility.ModeHistoryEnabled | visibility.ModeCurrentEnabled}})
	}
	if opts.Zoom != 0 && opts.Zoom != camera.ZoomUnit {
		mx, my := battleViewCentre(b.cam)
		jumpBattleZoom(b, mx, my, opts.Zoom, modernRenderer(opts))
	}
	return meta, nil
}

// liveFlightLimit is how many flight snapshots a trace of menu play may
// write: a session of play is far longer than a timed run.
const liveFlightLimit = 32

// liveTraceOptions builds the window's frame trace for a --live-trace run.
// current returns the shell the window holds when it is called, which a
// content reload in menu play can replace.
func liveTraceOptions(opts Options, current func() *gameShell, meta map[string]any) *ebitenapp.FrameTraceOptions {
	if opts.LiveTrace == "" {
		return nil
	}
	if meta == nil {
		meta = map[string]any{}
	}
	shell := current()
	flightLimit := 0
	if opts.Map == "" {
		// The map, seed and settings of each game are the player's; the
		// census names the battle's tick and the rows carry the cap in force.
		meta["mode"] = "menu play"
		flightLimit = liveFlightLimit
	} else {
		meta["map"] = opts.Map
		meta["seed"] = opts.Seed
	}
	meta["fps_cap"] = shell.presentation.FPS
	meta["unpaced"] = opts.LiveUnpaced
	meta["display"] = loadedSettings().Display
	meta["effects"] = presentationEffects(shell.presentation)
	meta["zoom"] = opts.Zoom.Float()
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" || setting.Key == "vcs.modified" {
				meta[setting.Key] = setting.Value
			}
		}
	}
	census := func() any {
		b := current().battle
		if b == nil || b.sess == nil || b.sess.Snapshot == nil {
			return nil
		}
		slow := b.takeSlowBatches()
		// The simulation may be running a batch: read only a pinned
		// publication, never the session.
		f, prev := b.sess.Snapshot.PinLatest()
		if f == nil {
			return nil
		}
		defer b.sess.Snapshot.Unpin(f)
		if prev != nil {
			defer b.sess.Snapshot.Unpin(prev)
		}
		inView := 0
		visible := func(x, y, z numeric.Fixed) bool {
			sx, sy := b.cam.WorldToScreen(x, y, z)
			return sx >= camera.OriginX && sx < b.cam.ViewW && sy >= camera.OriginY && sy < b.cam.ViewH
		}
		building := 0
		for _, u := range f.Units {
			if visible(u.X, u.Y, u.Z) {
				inView++
			}
			if u.BuildRemaining > 0 {
				building++
			}
		}
		burning := 0
		for _, feature := range f.Features {
			if feature.IsBurning {
				burning++
			}
		}
		out := map[string]any{"tick": f.Tick, "units": len(f.Units), "in_view_units": inView, "nanoframes": building,
			"projectiles": len(f.Projectiles), "effects": len(f.Effects), "fragments": len(f.Fragments), "burning_features": burning}
		if len(slow) > 0 {
			out["slow_sim"] = slow
		}
		return out
	}
	return &ebitenapp.FrameTraceOptions{
		Directory: opts.LiveTrace, Seconds: opts.LiveSeconds,
		ProfileFrom: opts.LiveProfileFrom, ExecTraceSeconds: opts.LiveExecTrace, Flight: opts.LiveFlight, FlightLimit: flightLimit,
		Census: census, Metadata: meta, Unpaced: opts.LiveUnpaced,
		Battle: func() bool { return current().battle != nil },
	}
}

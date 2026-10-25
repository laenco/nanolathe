package main

import (
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/nanolathe-gg/nanolathe/internal/mapgen"
	"github.com/nanolathe-gg/nanolathe/internal/settings"
)

// runMapgenShot captures the map generator's panel for review, as --nl-shot
// captures the Nanolathe screen (`--nl-shot DIR --nl-shot-only generator`):
// the panel with a finished four-player map of the world with most maps,
// and its Save form (docs/DESIGN_MAP_GENERATOR.md §10).
func runMapgenShot(opts Options, cs *contentSet, w, h int) error {
	shell, err := newGameShell(opts, cs)
	if err != nil {
		return err
	}
	shell.applySettings(settings.Defaults())
	s := newMapgenScreen(func() *gameShell { return shell })
	previous := mapgenScreenInst
	mapgenScreenInst = s
	defer func() { s.release(); mapgenScreenInst = previous }()
	s.p = mapgen.Params{Players: 4, Size: 10, Water: 1, Relief: 2, Metal: 1, Seed: 7}
	shell.openMapGenerator()
	game := &mapgenShotGame{s: s, out: opts.NLShot, w: w, h: h}
	ebiten.SetWindowVisible(false)
	ebiten.SetWindowSize(640, 360)
	if err := ebiten.RunGame(game); err != nil && !errors.Is(err, ebiten.Termination) {
		return fmt.Errorf("nanolathe: nl-shot: capture loop: %w", err)
	}
	if game.err != nil {
		return game.err
	}
	fmt.Fprintf(os.Stderr, "nanolathe: nl-shot: wrote %d generator captures to %s\n", game.written, opts.NLShot)
	return nil
}

type mapgenShotGame struct {
	s       *mapgenScreen
	out     string
	w, h    int
	target  *ebiten.Image
	frames  int
	written int
	err     error
	done    bool
}

func (g *mapgenShotGame) Update() error {
	if g.done || g.err != nil {
		return ebiten.Termination
	}
	return nil
}

func (g *mapgenShotGame) Layout(int, int) (int, int) { return 640, 360 }

func (g *mapgenShotGame) Draw(screen *ebiten.Image) {
	if g.done || g.err != nil {
		return
	}
	if g.target == nil {
		g.target = ebiten.NewImage(g.w, g.h)
	}
	g.target.Clear()
	g.s.Draw(g.target)
	screen.DrawImage(g.target, nil)
	r, running, _, _, _, err := g.s.job.state()
	switch {
	case err != nil && !running:
		g.err = fmt.Errorf("nanolathe: nl-shot: generator: %w", err)
		return
	case r == nil || running:
		if waiting := g.s.job.waiting(); waiting {
			g.s.regenerate()
		}
		return
	}
	// A few frames settle the picture, made on the first draw of a result.
	if g.frames++; g.frames < 3 {
		return
	}
	g.frames = 0
	name := "generator"
	if g.s.naming {
		name = "generator-save"
	}
	if g.err = g.write(name); g.err != nil {
		return
	}
	if g.s.naming {
		g.done = true
		return
	}
	g.s.startNaming()
}

func (g *mapgenShotGame) write(name string) error {
	pix := make([]byte, 4*g.w*g.h)
	g.target.ReadPixels(pix)
	for i := 3; i < len(pix); i += 4 {
		pix[i] = 255
	}
	img := &image.RGBA{Pix: pix, Stride: 4 * g.w, Rect: image.Rect(0, 0, g.w, g.h)}
	f, err := os.Create(filepath.Join(g.out, name+".png"))
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	g.written++
	return f.Close()
}

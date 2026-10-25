//go:build retail

package main

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/frame"
	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/hud"
	"github.com/nanolathe-gg/nanolathe/internal/testsupport"
)

// Optional visual inspection with the installed font, logo and shade tables;
// generated images stay outside the repository. No retail bytes are fixtures.
func TestRetailDefeatedScorePanelCapture(t *testing.T) {
	dir := os.Getenv("NANOLATHE_SCORE_CAPTURE_DIR")
	if dir == "" {
		t.Skip("NANOLATHE_SCORE_CAPTURE_DIR is unset")
	}
	cs, err := openContent(Options{Root: testsupport.RetailRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{3, 10} {
		for _, mode := range []gameplay.Mode{gameplay.Modern, gameplay.Strict31} {
			var rows [frame.PlayerRowSlots]frame.PlayerRow
			for i := 0; i < count; i++ {
				rows[i] = frame.PlayerRow{Present: true, Controller: 2, Name: fmt.Sprintf("Player %d", i+1), Logo: uint8(i), LiveUnits: i % 2, Kills: 13 * i, Losses: 7 * i, Rank: uint8(i)}
			}
			rows[0].Controller = 1 // an eliminated local row also stays readable
			if count == 3 {
				rows[0].Name, rows[1].Name, rows[2].Name = "Player", "TALON", "Rumad"
			}
			rows[0].Side = 2
			rows[1].Side = 0
			rows[2].Side = 1
			c, h, b := scorePanelFixtureWith(t, false, rows, 0, count)
			b.cat = &content.Catalog{Sides: []*content.SideDef{{Name: "TALON"}, {Name: "Rumad"}, {Name: "TLL"}}}
			b.sess.Gameplay = mode
			h.modalFont = loadGAFFontOptional(cs.fs, "anims/hattfont12.gaf", "score capture font")
			h.logos = loadGAFOptional(cs.fs, "textures/logos.gaf", "score capture logos")
			if h.modalFont == nil || h.logos == nil {
				t.Fatal("score capture font or logos unavailable")
			}
			h.pal = retailPaletteForTest(t, cs)
			c.SetPalette(h.pal)
			h.score = hud.ScorePanelWidth
			b.panelHoldFlag = true
			img := c.ComposeFrame()
			out := filepath.Join(dir, fmt.Sprintf("score-%s-%d.png", mode, count))
			file, err := os.Create(out)
			if err != nil {
				t.Fatal(err)
			}
			encodeErr := png.Encode(file, img)
			closeErr := file.Close()
			if encodeErr != nil || closeErr != nil {
				t.Fatalf("write capture: encode=%v close=%v", encodeErr, closeErr)
			}
			t.Log(out)
		}
	}
}

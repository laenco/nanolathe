package mapgen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/formats"
)

// testWorld is an authored world: one source map of rolling land round a
// lake, whose tiles are flat colours by what the cells under them are —
// blue water, land shaded by height — so that every kind of ground the
// generator asks for has art, and art always agrees with its heights.
func testWorld(t *testing.T) World {
	t.Helper()
	const w, h, sea = 96, 96, 40
	src := Source{Name: "testland", Width: w, Height: h, SeaLevel: sea, Heights: make([]byte, w*h)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := int32(sin256(x*5))*50>>16 + int32(cos256(y*7))*50>>16 + int32(sin256((x+y)*3))*40>>16
			dx, dy := x-w/2, y-h/2
			if d := isqrt(dx*dx + dy*dy); d < 18 {
				v -= int32(18-d) * 6 // the lake
			}
			src.Heights[y*w+x] = byte(min(max(v+110, 0), 255))
		}
	}
	tw, th := w/2, h/2
	src.TileIndices = make([]uint16, tw*th)
	for ty := 0; ty < th; ty++ {
		for tx := 0; tx < tw; tx++ {
			n := ty*tw + tx
			src.TileIndices[n] = uint16(n)
			art := make([]byte, 1024)
			for py := 0; py < 32; py++ {
				for px := 0; px < 32; px++ {
					hgt := src.Heights[(2*ty+py/16)*w+2*tx+px/16]
					if hgt < sea {
						art[py*32+px] = 10
					} else {
						art[py*32+px] = 64 + hgt/2
					}
				}
			}
			src.TileGraphics = append(src.TileGraphics, art...)
		}
	}
	var pal [256][3]byte
	for i := range pal {
		pal[i] = [3]byte{byte(i / 2), byte(i), byte(i / 3)}
	}
	pal[10] = [3]byte{20, 40, 200}
	return World{
		Name:    "Test World",
		Sources: []Source{src},
		Deposit: Feature{Name: "TestMetal", FootX: 3, FootZ: 3, Metal: 100},
		Trees:   []Feature{{Name: "TestTree", FootX: 1, FootZ: 1, Blocking: true, Weight: 3}},
		Foliage: []Feature{{Name: "TestFern", FootX: 1, FootZ: 1, Weight: 1}},
		Rocks:   []Feature{{Name: "TestRock", FootX: 1, FootZ: 1, Metal: 20, Blocking: true}},
		Classes: []MoveClass{{Name: "testtank", FootX: 3, FootZ: 3, MaxSlope: 15, BadSlope: 7,
			MaxWaterSlope: 15, BadWaterSlope: 7, MaxWaterDepth: 12, MinWaterDepth: -10000}},
		Palette: &pal,
		MinWind: 100, MaxWind: 2000, Gravity: 112, Solar: 20, SurfaceMetal: 3, MohoMetal: 30,
	}
}

func generate(t *testing.T, w World, p Params) *Map {
	t.Helper()
	g, err := NewGenerator(w)
	if err != nil {
		t.Fatal(err)
	}
	m, err := g.Generate(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// One code is one map: the candidates run in parallel, and how many run at
// once must not change which one wins or what it looks like.
func TestGenerateIsDeterministic(t *testing.T) {
	w := testWorld(t)
	p := Params{Players: 4, Size: 6, Water: 1, Relief: 2, Metal: 1, Seed: 7}
	a := generate(t, w, p)
	old := runtime.GOMAXPROCS(1)
	b := generate(t, w, p)
	runtime.GOMAXPROCS(old)
	g, err := NewGenerator(w)
	if err != nil {
		t.Fatal(err)
	}
	g.Workers = 1
	c, err := g.Generate(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	g.Workers = 3
	d, err := g.Generate(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range []*Map{b, c, d} {
		if !bytes.Equal(a.TNT, other.TNT) || !reflect.DeepEqual(a.Balance, other.Balance) {
			t.Fatalf("the same parameters made different maps: balance %+v vs %+v", a.Balance, other.Balance)
		}
	}
	if c := generate(t, w, Params{Players: 4, Size: 6, Water: 1, Relief: 2, Metal: 1, Seed: 8}); bytes.Equal(a.TNT, c.TNT) {
		t.Fatal("another seed made the same map")
	}
}

// The files read back as the engine reads maps, with the starts in the
// numbered order the lobby assigns and the deposits in their anchor cells.
func TestGeneratedMapReadsBack(t *testing.T) {
	w := testWorld(t)
	m := generate(t, w, Params{Players: 4, Teams: 2, Size: 6, Water: 1, Relief: 1, Metal: 1, Seed: 3})
	tnt, err := formats.LoadTNT(m.TNT)
	if err != nil {
		t.Fatal(err)
	}
	if int(tnt.Width) != m.Width || int(tnt.Height) != m.Height || int(tnt.Width) != 6*cellsPerUnit+voidRight || tnt.SeaLevel != seaLevel {
		t.Fatalf("terrain %dx%d sea %d, want %dx%d sea %d", tnt.Width, tnt.Height, tnt.SeaLevel, 6*cellsPerUnit+voidRight, 6*cellsPerUnit+voidBottom, seaLevel)
	}
	// Every deposit stands on level ground, so its extractor can be built.
	deposits := 0
	for i, a := range tnt.Attributes {
		if int(a.Feature) >= len(tnt.FeatureTable) || tnt.FeatureTable[a.Feature].Name != w.Deposit.Name {
			continue
		}
		deposits++
		x, y := i%m.Width, i/m.Width
		lo, hi := byte(255), byte(0)
		for yy := y; yy <= y+w.Deposit.FootZ; yy++ {
			for xx := x; xx <= x+w.Deposit.FootX; xx++ {
				h := tnt.Attributes[yy*m.Width+xx].Height
				lo, hi = min(lo, h), max(hi, h)
			}
		}
		if hi != lo {
			t.Errorf("the deposit at %d,%d stands on corners %d..%d", x, y, lo, hi)
		}
	}
	if deposits != m.Deposits || deposits < 4*(depositsNear[1]+depositsMiddle[1]) {
		t.Fatalf("%d deposits in the terrain, map reports %d, want at least %d", deposits, m.Deposits, 4*(depositsNear[1]+depositsMiddle[1]))
	}
	data, err := m.OTA("Test Map", "A generated test map")
	if err != nil {
		t.Fatal(err)
	}
	ota, err := formats.LoadOTA(data)
	if err != nil {
		t.Fatal(err)
	}
	if ota.MissionName != "Test Map" || ota.NumPlayers != "4" || ota.Size != "6 x 6" || len(ota.Schemas) != 1 || !ota.HasNetworkSchema() {
		t.Fatalf("OTA reads back as %q, %q players, size %q, %d schemas", ota.MissionName, ota.NumPlayers, ota.Size, len(ota.Schemas))
	}
	if planet, _ := ota.Global.StringValue("planet", ""); planet != w.Name {
		t.Fatalf("planet %q, want %q", planet, w.Name)
	}
	specials := ota.Schemas[0].Section.Section("specials").Sections()
	if len(specials) != len(m.Starts) {
		t.Fatalf("%d specials for %d starts", len(specials), len(m.Starts))
	}
	for i, sp := range specials {
		what, _ := sp.StringValue("specialwhat", "")
		if want := "StartPos" + string(rune('1'+i)); what != want || int(sp.IntValue("XPos", -1)) != m.Starts[i].X || int(sp.IntValue("ZPos", -1)) != m.Starts[i].Z {
			t.Fatalf("special %d is %s at %d,%d; want %s at %+v", i, what, sp.IntValue("XPos", -1), sp.IntValue("ZPos", -1), want, m.Starts[i])
		}
	}
}

// Every player is offered the same metal: the deposits go at the same
// travel costs from every base, so the metal within each one's reach is
// equal however the terrain falls.
func TestEveryPlayerHasTheSameMetal(t *testing.T) {
	w := testWorld(t)
	for _, p := range []Params{
		{Players: 2, Size: 6, Relief: 2, Metal: 0, Seed: 11},
		{Players: 3, Size: 8, Water: 1, Relief: 1, Metal: 2, Seed: 12},
		{Players: 4, Teams: 2, Size: 8, Relief: 3, Metal: 1, Seed: 13},
	} {
		if m := generate(t, w, p); m.Balance.Metal.Spread != 0 {
			t.Errorf("%+v: metal spread %d per mille, want 0", p, m.Balance.Metal.Spread)
		}
	}
}

func TestCodeRoundTrips(t *testing.T) {
	p := Params{Players: 6, Teams: 3, Size: 14, Water: 2, Relief: 1, Metal: 2, Seed: 1<<63 + 12345}
	code := Code("Green Planet", p)
	if !strings.HasPrefix(code, "green_planet-6-3-14-2-1-2-") || !strings.HasSuffix(code, "-v1") {
		t.Fatalf("code %q", code)
	}
	world, got, err := ParseCode(code)
	if err != nil || world != "green_planet" || got != p {
		t.Fatalf("ParseCode(%q) = %q, %+v, %v; want green_planet, %+v", code, world, got, err, p)
	}
	if _, _, err := ParseCode(strings.TrimSuffix(code, "v1") + "v0"); err == nil {
		t.Fatal("a code of another generator version was accepted")
	}
	if n := (Params{Players: 4, Teams: 3}).Normalize(); n.Teams != 0 {
		t.Fatalf("4 players in 3 teams normalized to %d teams, want free for all", n.Teams)
	}
}

// A dry world cannot paint water, so the generator refuses rather than
// painting land art over the sea.
func TestDryWorldRefusesWater(t *testing.T) {
	w := testWorld(t)
	src := w.Sources[0]
	src.SeaLevel = 0
	w.Sources = []Source{src}
	g, err := NewGenerator(w)
	if err != nil {
		t.Fatal(err)
	}
	if g.Wet() {
		t.Fatal("a world without water reports water to paint")
	}
	if _, err := g.Generate(context.Background(), Params{Players: 2, Size: 6, Water: 1}, nil); err == nil {
		t.Fatal("a dry world generated a map with water")
	}
}

// A world whose maps draw no deposit — its ground is the metal — makes maps
// without deposits, every player's metal then equal by the ground.
func TestWorldWithoutDepositsMakesNone(t *testing.T) {
	w := testWorld(t)
	w.Deposit = Feature{}
	m := generate(t, w, Params{Players: 3, Size: 6, Relief: 1, Metal: 2, Seed: 4})
	if m.Deposits != 0 || m.Balance.Metal.Spread != 0 {
		t.Fatalf("%d deposits, metal spread %d", m.Deposits, m.Balance.Metal.Spread)
	}
}

// The balance gives each measurement per start, in start order, with the
// spread the ranking reads, and every start of the chosen layout is usable.
func TestBalanceMeasuresEveryStart(t *testing.T) {
	w := testWorld(t)
	p := Params{Players: 3, Size: 8, Water: 1, Relief: 2, Metal: 1, Seed: 21}
	m := generate(t, w, p)
	for name, ms := range map[string]Measure{"path": m.Balance.Path, "room": m.Balance.Room, "metal": m.Balance.Metal} {
		if len(ms.Values) != p.Players || ms.Spread != spread(ms.Values) {
			t.Errorf("%s: %+v for %d players", name, ms, p.Players)
		}
	}
	for i, room := range m.Balance.Room.Values {
		if room < minRoom {
			t.Errorf("start %d has %d cells of room, under the %d a usable start has", i, room, minRoom)
		}
	}
	if want := 4*m.Balance.Path.Spread + 2*m.Balance.Room.Spread + 3*m.Balance.Metal.Spread; m.Balance.Score != want {
		t.Errorf("score %d, want %d", m.Balance.Score, want)
	}
}

// A start in water, or one short of building room, is not usable.
func TestStartUsable(t *testing.T) {
	tr := &terrain{w: 4, h: 4, sea: 10, height: make([]int32, 16)}
	for i := range tr.height {
		tr.height[i] = 20
	}
	s := point{x: 1, y: 1}
	if !startUsable(tr, s, minRoom) {
		t.Error("a dry start with the least room was refused")
	}
	if startUsable(tr, s, minRoom-1) {
		t.Error("a cramped start was accepted")
	}
	tr.height[1*4+1] = 5
	if startUsable(tr, s, minRoom*2) {
		t.Error("a start in water was accepted")
	}
}

// A cancelled request returns its context's error and never a map, before
// any work or partway through it.
func TestCancelledGenerateReturnsNoMap(t *testing.T) {
	g, err := NewGenerator(testWorld(t))
	if err != nil {
		t.Fatal(err)
	}
	p := Params{Players: 4, Size: 8, Water: 1, Relief: 2, Metal: 1, Seed: 5}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if m, err := g.Generate(ctx, p, nil); m != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled before it began: map %v, error %v", m != nil, err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	m, err := g.Generate(ctx, p, func(done, _ int) {
		if done == 1 {
			cancel()
		}
	})
	if m != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled partway: map %v, error %v", m != nil, err)
	}
}

// One code on the authored test world always makes these exact files. The
// generator uses integer arithmetic only and its own random stream, so every
// platform must agree; a deliberate change to what it makes changes the
// hash and Version together.
func TestGeneratedMapIsPinned(t *testing.T) {
	m := generate(t, testWorld(t), Params{Players: 4, Teams: 2, Size: 8, Water: 1, Relief: 2, Metal: 1, Seed: 1234567})
	ota, err := m.OTA("Pinned", "The pinned test map")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(append(append([]byte(nil), m.TNT...), ota...))
	if got := hex.EncodeToString(sum[:]); got != pinnedMapHash {
		t.Fatalf("the pinned code made %s, want %s", got, pinnedMapHash)
	}
}

const pinnedMapHash = "806ec22240d0652f995918977e19f64859bf00704fa37350ce24d82e3e2ea96f"

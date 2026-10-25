package main

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/nanolathe-gg/nanolathe/internal/client"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/drawlist"
	"github.com/nanolathe-gg/nanolathe/internal/frame"
	"github.com/nanolathe-gg/nanolathe/internal/model"
	"github.com/nanolathe-gg/nanolathe/internal/palette"
	"github.com/nanolathe-gg/nanolathe/internal/platform/gpurender"
	"github.com/nanolathe-gg/nanolathe/internal/poseblend"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

// The viewer projects detached production geometry at its final raster scale.
// Its frozen Create-pose pivot keeps framing independent of changing bounds.
// No battle cache or source vertex is changed (DESIGN_DEVELOPER_TOOLS §7).
type unitViewerModel struct {
	// Enhanced Move smoothing; the standalone comparison can show raw poses.
	walkEnabled                         bool
	walkSmooth                          bool
	walkAnim                            *unitViewerAnimation
	walkHistory                         poseblend.History
	walkPrevious, walkCurrent, walkView []frame.PieceView
	preview                             *client.ModelPreviewRenderer
	gpu                                 *gpurender.Renderer
	image                               *ebiten.Image
	err                                 error
	radius                              float64
	pivot                               [3]numeric.Fixed // root-local center of the frozen Create fit
	fitRoot                             model.PieceState
	key                                 unitViewerModelKey
	poses                               []frame.PieceView
	poseNote                            string
	def                                 *content.UnitDef
	geometry                            *model.Model
	anim                                *unitViewerAnimation
	action                              unitViewerAction
	weapon                              int
	severity                            int32
	power                               int8
	// features is the immutable catalog's feature table, through which the
	// Wreck view resolves corpses [06 §12.2].
	features map[string]*content.FeatureDef
	wreck    unitViewerWreckModel
	palette  *palette.Tables // the last record's palette, for an empty stage
	// mount loads a factory's product models; builds and hidden are the
	// selected unit's Build-tab entries and the hidden records. speed is the
	// product cycle's preview speed.
	mount  *vfs.FS
	builds []unitViewerLink
	hidden map[*content.UnitDef]bool
	speed  int
	// view is the last unit record's projection, for the spray overlay.
	view unitViewerView
	// gen counts action choices and unit selections; the field stages a
	// fresh battle for each (unit_viewer_field.go).
	gen uint64
}

// unitViewerView is the projection, canvas and orientation of the last unit
// record. The spray projects through exactly these.
type unitViewerView struct {
	ok                  bool
	projection          client.ModelPreviewProjection
	w, h                int
	heading, tilt, bank uint16
}

// unitViewerWreckModel caches the corpse feature model the Wreck view draws,
// with its own fixed fit. err reports a missing or undrawable model; nothing
// is substituted for it (DESIGN_DEVELOPER_TOOLS §7).
type unitViewerWreckModel struct {
	feature  *content.FeatureDef
	geometry *model.Model
	radius   float64
	pivot    [3]numeric.Fixed
	fitRoot  model.PieceState
	err      error
}

type unitViewerModelKey struct {
	def        *content.UnitDef
	feature    *content.FeatureDef
	yaw, pitch uint16
	zoom       float64
	w, h       int
}

func (m *unitViewerModel) selectUnit() {
	m.gen++
	m.err, m.radius, m.image, m.key = nil, 0, nil, unitViewerModelKey{}
	m.pivot, m.fitRoot = [3]numeric.Fixed{}, model.PieceState{}
	m.poses, m.poseNote = nil, ""
	m.def, m.geometry, m.anim = nil, nil, nil
	m.walkAnim, m.walkHistory = nil, poseblend.History{}
	m.walkPrevious, m.walkCurrent, m.walkView = nil, nil, nil
	m.action, m.weapon, m.severity, m.power = unitViewerIdle, 1, 0, 0
	m.wreck = unitViewerWreckModel{}
	m.builds, m.view = nil, unitViewerView{}
}

func (m *unitViewerModel) release() {
	if m.gpu != nil {
		m.gpu.ResetSources()
	}
	*m = unitViewerModel{}
}

func (m *unitViewerModel) draw(cs *contentSet, def *content.UnitDef, yaw, pitch, zoom float64, w, h int) *ebiten.Image {
	if def == nil || cs == nil || m.err != nil {
		return nil
	}
	m.mount = cs.unmappedMount
	key := unitViewerModelKey{def, m.anim.wreckFeature(), uint16(int64(yaw)), uint16(int64(pitch)), zoom, w, h}
	if m.key == key && (m.image != nil || key.feature != nil) {
		return m.image
	}
	if m.preview == nil {
		m.preview, m.err = client.NewModelPreviewRenderer(cs.unmappedMount, cs.presentation.TeamLogos)
		if m.err != nil {
			return nil
		}
	}
	if m.radius == 0 {
		path := vfs.ResourcePath("objects3d", def.ObjectName, "3do")
		mdl, err := model.Load(cs.unmappedMount, path)
		if err != nil {
			m.err = err
			return nil
		}
		m.loadAnimation(def, mdl)
		key.feature = m.anim.wreckFeature()
	}
	heading, tilt, bank := unitViewerOrientation(key.yaw, key.pitch)
	var record client.ModelPreviewRecord
	var err error
	m.view.ok = false
	if m.anim == nil || m.anim.action != unitViewerWreck {
		if unitViewerAllHidden(m.poses) {
			// A death that exploded every piece leaves nothing to draw; the
			// stage keeps its backdrop and the status says why.
			return m.drawEmpty(cs, key, w, h)
		}
	}
	if m.anim != nil && m.anim.action == unitViewerWreck {
		// The Wreck view draws the corpse feature's own model. A missing
		// corpse or model leaves the stage empty and the status says why.
		if key.feature == nil || !m.loadWreck(cs, key.feature) {
			m.image, m.key = nil, key
			return nil
		}
		record, w, h, err = m.recordWreck(heading, tilt, bank, zoom, w, h)
		if err != nil {
			m.wreck.err = err
			m.image, m.key = nil, key
			return nil
		}
	} else if record, w, h, err = m.record(def, heading, tilt, bank, zoom, w, h); err != nil {
		m.err = err
		return nil
	}
	m.palette = record.Palette
	if m.gpu == nil {
		m.gpu, m.err = gpurender.NewChecked(record.Palette, w, h)
		if m.err != nil {
			return nil
		}
	}
	var list drawlist.List
	list.RecordClear()
	unitViewerBackdrop(&list, record.Palette, w, h)
	m.image = m.gpu.Execute(&list, w, h)
	if m.image == nil {
		m.err = fmt.Errorf("nanolathe: unit viewer rendering failed: logical path objects3d/%s.3do, providers searched [mounted content], expected model surface", def.ObjectName)
		return nil
	}
	m.gpu.Expand()
	if m.err = m.gpu.DrawModelPreview(m.image, record.Projected); m.err != nil {
		return nil
	}
	m.key = key
	return m.image
}

func unitViewerAllHidden(poses []frame.PieceView) bool {
	if len(poses) == 0 {
		return false
	}
	for _, p := range poses {
		if !p.Hidden {
			return false
		}
	}
	return true
}

// drawEmpty draws the stage backdrop alone.
func (m *unitViewerModel) drawEmpty(cs *contentSet, key unitViewerModelKey, w, h int) *ebiten.Image {
	if m.palette == nil {
		if m.palette, m.err = palette.Load(cs.unmappedMount); m.err != nil {
			return nil
		}
	}
	if m.gpu == nil {
		if m.gpu, m.err = gpurender.NewChecked(m.palette, w, h); m.err != nil {
			return nil
		}
	}
	var list drawlist.List
	list.RecordClear()
	unitViewerBackdrop(&list, m.palette, w, h)
	if m.image = m.gpu.Execute(&list, w, h); m.image != nil {
		m.key = key
	}
	return m.image
}

// ensureLoaded loads the unit's model and creation fit outside Draw, for a
// capture that scripts actions before its first frame.
func (m *unitViewerModel) ensureLoaded(cs *contentSet, def *content.UnitDef) bool {
	if def == nil || cs == nil || m.err != nil {
		return false
	}
	m.mount = cs.unmappedMount
	if m.radius == 0 {
		mdl, err := model.Load(cs.unmappedMount, vfs.ResourcePath("objects3d", def.ObjectName, "3do"))
		if err != nil {
			m.err = err
			return false
		}
		m.loadAnimation(def, mdl)
	}
	return true
}

// advance runs preview ticks without a host clock, within the per-update
// bound, for a reproducible capture.
func (m *unitViewerModel) advance(ticks int) {
	for range ticks {
		m.updateAnimation(1.0 / unitViewerTickRate)
	}
}

// adoptFieldWreck shows the corpse the field's death left: the Wreck view
// then draws that feature, at the depth its corpse chain gives it, instead
// of the presentation script's own answer. A death that left no corpse
// leaves the stage empty and says so; nothing is substituted
// (DESIGN_DEVELOPER_TOOLS §7).
func (m *unitViewerModel) adoptFieldWreck(cs *contentSet, def *content.UnitDef, corpse *content.FeatureDef, depth int) {
	if !m.ensureLoaded(cs, def) || m.anim == nil || m.anim.action != unitViewerWreck {
		return
	}
	a := m.anim
	a.death.done, a.death.corpse, a.death.depth, a.death.field = true, corpse, int32(depth), true
	a.note = a.wreckNote()
	m.refreshPose()
}

// loadWreck loads and fits the corpse feature's 3DO through the same model
// path as units. A sprite feature or an unloadable model is reported, never
// replaced (DESIGN_DEVELOPER_TOOLS §7).
func (m *unitViewerModel) loadWreck(cs *contentSet, feature *content.FeatureDef) bool {
	if m.wreck.feature == feature {
		return m.wreck.err == nil && m.wreck.geometry != nil
	}
	m.wreck = unitViewerWreckModel{feature: feature}
	object := strings.TrimSpace(feature.Object)
	if object == "" {
		m.wreck.err = fmt.Errorf("feature %s has no 3D object", feature.CanonicalKey)
		return false
	}
	mdl, err := model.Load(cs.unmappedMount, vfs.ResourcePath("objects3d", object, "3do"))
	if err != nil {
		m.wreck.err = err
		return false
	}
	m.wreck.geometry = mdl
	m.wreck.pivot, m.wreck.fitRoot, m.wreck.radius = unitViewerFit(mdl, nil)
	return true
}

func (m *unitViewerModel) record(def *content.UnitDef, heading, pitch, bank uint16, zoom float64, w, h int) (client.ModelPreviewRecord, int, int, error) {
	opts := client.ModelPreviewOptions{
		Model: vfs.ResourcePath("objects3d", def.ObjectName, "3do"), Width: w, Height: h,
		Heading: heading, Pitch: pitch, Bank: bank,
		Structure: def.BMCode == 0, KeyPlane: def.ZBuffer, PiecePoses: m.poses,
	}
	opts.Attachment = m.attachment(opts.Model)
	record, rw, rh, err := m.recordProjected(opts, m.radius, m.orientedPivot(heading, pitch, bank), zoom)
	if err != nil && opts.Attachment != nil {
		// A product the renderer refuses is reported and skipped by the next
		// work step; the factory itself is still drawn.
		m.anim.product().failed = err
		opts.Attachment = nil
		record, rw, rh, err = m.recordProjected(opts, m.radius, m.orientedPivot(heading, pitch, bank), zoom)
	}
	if err == nil {
		m.view.heading, m.view.tilt, m.view.bank, m.view.ok = heading, pitch, bank, true
	}
	return record, rw, rh, err
}

// attachment is the factory's product on its pad: the battle's nanoframe
// look at the remaining fraction, posed by its own script, placed where the
// QueryBuildInfo piece hangs it. The pad is resolved again for every record
// because pads can turn (DESIGN_GPU_RENDERER §22.5).
func (m *unitViewerModel) attachment(parent string) *client.ModelPreviewAttachment {
	c := m.anim.product()
	if c == nil || c.failed != nil || m.preview == nil || m.geometry == nil {
		return nil
	}
	g := &m.anim.build
	var placement client.ModelPreviewPlacement
	if !g.origin {
		// A pad answer past the script's declared pieces but inside the
		// model places nothing; see the spray's target.
		if g.pad < 0 || g.pad >= len(m.geometry.Pieces) {
			return nil
		}
		var err error
		if placement, err = m.preview.PiecePlacement(parent, m.poses, m.geometry.Pieces[g.pad].Name); err != nil {
			c.failed = err
			return nil
		}
	}
	return &client.ModelPreviewAttachment{
		Model:     vfs.ResourcePath("objects3d", c.def.ObjectName, "3do"),
		Structure: c.def.BMCode == 0, KeyPlane: c.def.ZBuffer,
		PiecePoses:     c.anim.poses(),
		Placement:      placement,
		BuildRemaining: c.remaining,
		NanoframeID:    c.id,
		NanoframeTick:  uint32(m.anim.ticks),
	}
}

// recordWreck draws the corpse as the battle draws a 3DO feature: through
// the structure path with the height plane, in its authored pose
// [03 R-REN-03A §2].
func (m *unitViewerModel) recordWreck(heading, pitch, bank uint16, zoom float64, w, h int) (client.ModelPreviewRecord, int, int, error) {
	opts := client.ModelPreviewOptions{
		Model: vfs.ResourcePath("objects3d", strings.TrimSpace(m.wreck.feature.Object), "3do"), Width: w, Height: h,
		Heading: heading, Pitch: pitch, Bank: bank, Structure: true, KeyPlane: true,
	}
	pivot := unitViewerOrientedPivot(m.wreck.geometry, m.wreck.fitRoot, m.wreck.pivot, heading, pitch, bank)
	return m.recordProjected(opts, m.wreck.radius, pivot, zoom)
}

func (m *unitViewerModel) recordProjected(opts client.ModelPreviewOptions, radius float64, pivot [3]numeric.Fixed, zoom float64) (client.ModelPreviewRecord, int, int, error) {
	w, h := opts.Width, opts.Height
	// 2*sqrt(1.25) bounds the diameter under the half-height projection
	// [03 §2.5]. Both fit and pivot stay fixed while animations move the body.
	projection := client.ModelPreviewProjection{
		PixelsPerUnit: 0.94 * float64(min(w, h)) / (2 * math.Sqrt(1.25) * radius) * zoom,
		Pivot:         pivot,
	}
	record, err := m.preview.RecordProjectedGeometry(opts, projection)
	if err != nil {
		return record, w, h, err
	}
	factor, rw, rh := unitViewerRasterSize(record.List.ModelCommands(), 1, w, h)
	if factor < 1 {
		// Re-project from fixed-point vertices at the reduced raster scale.
		// Scaling rounded packets would reintroduce amplified rounding jumps.
		projection.PixelsPerUnit *= factor
		opts.Width, opts.Height = rw, rh
		record, err = m.preview.RecordProjectedGeometry(opts, projection)
	}
	m.view.projection, m.view.w, m.view.h = projection, rw, rh
	return record, rw, rh, err
}

// Orbit first, then tilt around the display's horizontal axis. Directly
// passing these as body heading/pitch applies tilt before yaw [03 §2.4], C21,
// which makes a turning model lean from side to side. Decompose the viewer's
// X*Y transform into the production Y*X*Z orientation (host policy, §7).
func unitViewerOrientation(yaw, pitch uint16) (heading, tilt, bank uint16) {
	const radians = 2 * math.Pi / 65536
	sy, cy := math.Sincos(float64(yaw) * radians)
	sp, cp := math.Sincos(float64(pitch) * radians)
	x := math.Asin(max(-1, min(1, sp*cy)))
	y, z := math.Atan2(sy, cp*cy), math.Atan2(-sp*sy, cp)
	if math.Abs(math.Cos(x)) < 1e-8 {
		// At a vertical pole, heading and bank share an axis. Fix bank to
		// zero and retain the equivalent heading, avoiding atan2(0, 0).
		y, z = math.Atan2(cp*sy, cy), 0
	}
	angle := func(a float64) uint16 { return uint16(int64(math.Round(a / radians))) }
	return angle(y), angle(x), angle(z)
}

// Bound both the projected geometry and the 2x preview canvas to 4096 pixels
// (DESIGN_GPU_RENDERER §22.5). Reduce geometry and canvas together; the UI's
// final stretch preserves zoom and aspect without rounding vertices first.
func unitViewerRasterSize(commands []drawlist.Model, scale float64, w, h int) (float64, int, int) {
	bound := float64(max(1, w, h))
	for _, cmd := range commands {
		g := cmd.Geometry
		if cmd.ShadowOnly || g == nil {
			continue
		}
		bound = max(bound, float64(max(g.Width, g.Height)))
		if ss := g.Supersample; ss != nil {
			bound = max(bound, float64(max(ss.Width, ss.Height))/2)
		}
	}
	// 2000 leaves room for independent integer rounding and atlas padding.
	f := min(1, 2000/(scale*bound))
	return scale * f, max(1, int(float64(w)*f)), max(1, int(float64(h)*f))
}

// This is a presentation fit bound over drawable authored vertices, including
// parent translations. Selection plates and attachment-only points must not
// make the visible body tiny [03 §2.4][03 §2.4.1].
func unitViewerBounds(m *model.Model, states []model.PieceState) (lo, hi [3]float64, found bool) {
	lo = [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
	hi = [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	for i, p := range m.Pieces {
		// Fit the same independently visible pieces the renderer draws; a
		// hidden pivot still positions its visible children [03 §2.4.1].
		if i < len(states) && states[i].Hidden {
			continue
		}
		transform := model.Compose(m, states, i)
		for fi, face := range p.Primitives {
			if p.Selection && fi == 0 {
				continue
			}
			if len(face.VertexIndices) < 3 || (face.IsColored&1 == 0 && (len(face.VertexIndices) != 4 || face.TextureName == "")) {
				continue
			}
			for _, vi := range face.VertexIndices {
				if int(vi) >= len(p.Vertices) {
					continue
				}
				v := transform.ApplyVertex(p.Vertices[vi])
				for axis := range v {
					f := float64(v[axis]) / 65536
					lo[axis], hi[axis] = min(lo[axis], f), max(hi[axis], f)
				}
				found = true
			}
		}
	}
	return lo, hi, found
}

func unitViewerRadius(m *model.Model, states []model.PieceState) float64 {
	lo, hi, found := unitViewerBounds(m, states)
	if !found {
		return 1
	}
	x, y, z := hi[0]-lo[0], hi[1]-lo[1], hi[2]-lo[2]
	return max(1, math.Sqrt(x*x+y*y+z*z)/2)
}

// Freeze the reference in root-local coordinates so authored root offsets and
// rotations use the same hierarchy as the body. Neutralizing only the root
// leaves every child's creation pose in the fit; the root's frozen state is
// reapplied with each view's angles, independently of later animation.
func (m *unitViewerModel) fitCreatePose(states []model.PieceState) {
	m.pivot, m.fitRoot, m.radius = unitViewerFit(m.geometry, states)
}

func unitViewerFit(mdl *model.Model, states []model.PieceState) (pivot [3]numeric.Fixed, fitRoot model.PieceState, radius float64) {
	root := mdl.Root
	neutral := slices.Clone(states)
	if len(neutral) < len(mdl.Pieces) {
		neutral = append(neutral, make([]model.PieceState, len(mdl.Pieces)-len(neutral))...)
	}
	fitRoot = neutral[root]
	neutral[root].RotX, neutral[root].RotY, neutral[root].RotZ = 0, 0, 0
	neutral[root].Trans = [3]numeric.Fixed{}
	lo, hi, found := unitViewerBounds(mdl, neutral)
	if found {
		for axis := range pivot {
			pivot[axis] = numeric.Fixed(math.Round((lo[axis]+hi[axis])*32768)) - mdl.Pieces[root].Translate[axis]
		}
	}
	return pivot, fitRoot, unitViewerRadius(mdl, neutral)
}

func (m *unitViewerModel) orientedPivot(heading, pitch, bank uint16) [3]numeric.Fixed {
	return unitViewerOrientedPivot(m.geometry, m.fitRoot, m.pivot, heading, pitch, bank)
}

func unitViewerOrientedPivot(mdl *model.Model, fitRoot model.PieceState, pivot [3]numeric.Fixed, heading, pitch, bank uint16) [3]numeric.Fixed {
	root := mdl.Root
	states := make([]model.PieceState, root+1)
	states[root] = fitRoot
	model.FoldRootAngles(states, root, heading, pitch, bank)
	return model.Compose(mdl, states, root).Apply(pivot)
}

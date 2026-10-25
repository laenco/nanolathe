package session

import (
	"fmt"
	"testing"

	"github.com/nanolathe-gg/nanolathe/formats"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/features"
	"github.com/nanolathe-gg/nanolathe/internal/frame"
	"github.com/nanolathe-gg/nanolathe/internal/world"
)

// Publication is measured separately from dormant lifecycle work: it still
// visits the larger field, even though each unchanged view is reused.
func BenchmarkPublishWreckHeavyFeatures(b *testing.B) {
	for _, count := range []int{2048, 8192, 32768} {
		b.Run(fmt.Sprintf("wrecks=%d", count), func(b *testing.B) {
			const side = 256
			attrs := make([]formats.TNTAttribute, side*side)
			for i := range attrs {
				attrs[i] = formats.TNTAttribute{Height: 10, Feature: world.PlotFeatureNone}
			}
			terrain := &world.Terrain{CellW: side, CellH: side, Plot: world.ExpandPlot(attrs, side, side)}
			service := features.NewService(terrain, nil, nil, nil)
			service.Rules = features.ModernRules{}
			def := &content.FeatureDef{DefinitionHeader: content.DefinitionHeader{CanonicalKey: "wreck-benchmark"}, FootprintX: 1, FootprintZ: 1, Damage: 100, Object: "wreck", Blocking: true}
			for i := 0; i < count; i++ {
				if service.PlaceAt(i%side, i/side, def) == nil {
					b.Fatalf("wreck %d refused", i)
				}
			}
			service.TickLifecycle(1)
			s := &Session{Snapshot: frame.NewBuffer(), Features: service}
			for tick := uint32(1); tick <= 3; tick++ {
				s.publishSnapshot(tick)
			}
			if len(s.Snapshot.Current().Features) != count {
				b.Fatal("publication truncated the workload")
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.publishSnapshot(uint32(i + 4))
			}
		})
	}
}

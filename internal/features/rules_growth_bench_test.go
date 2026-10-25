package features

import (
	"fmt"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/world"
)

// Wrecks use real placement and dormancy. Counts name the measured workload;
// the benchmark refuses to measure a silently truncated feature field.
func wreckHeavyService(b *testing.B, count int) *Service {
	b.Helper()
	const side = 256
	s := NewService(newTestTerrainP1(side, side), nil, nil, nil)
	s.Rules = ModernRules{}
	def := defP1("wreck", 1, 1, "wreck", "")
	for i := 0; i < count; i++ {
		if s.PlaceAt(i%side, i/side, def) == nil {
			b.Fatalf("wreck %d refused", i)
		}
	}
	s.TickLifecycle(1)
	if s.arenaHeld != count || s.activeHead != nil {
		b.Fatalf("wreck field count=%d active=%v", s.arenaHeld, s.activeHead != nil)
	}
	return s
}

func BenchmarkFeatureWrecks(b *testing.B) {
	for _, count := range []int{2048, 8192, 32768} {
		b.Run(fmt.Sprintf("wrecks=%d", count), func(b *testing.B) {
			b.Run("settled-lifecycle", func(b *testing.B) {
				s := wreckHeavyService(b, count)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					s.TickLifecycle(uint32(i + 2))
				}
			})
			b.Run("spatial-query", func(b *testing.B) {
				s := wreckHeavyService(b, count)
				x, z := world.CellToWorld(128), world.CellToWorld(4)
				out := s.AppendInstancesNear(nil, x, z, 96)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					out = s.AppendInstancesNear(out[:0], x, z, 96)
				}
				if len(out) == 0 {
					b.Fatal("empty spatial workload")
				}
			})
			b.Run("replacement", func(b *testing.B) {
				s := wreckHeavyService(b, count)
				def := s.InstanceAt(0, 0).Def
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if s.PlaceAt(0, 0, def) == nil {
						b.Fatal("replacement refused")
					}
					s.TickLifecycle(uint32(i + 2))
				}
			})
		})
	}
}

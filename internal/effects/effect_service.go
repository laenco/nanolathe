package effects

import (
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/frame"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
)

// Event and Kind are local aliases for the ordered cue input; routing and admission belong to this authoritative service.
type Event = frame.Event
type Kind = frame.Kind

const (
	KindCOBSFX          = frame.KindCOBSFX
	KindNanolathe       = frame.KindNanolathe
	KindMuzzleFlash     = frame.KindMuzzleFlash
	KindSmokeStart      = frame.KindSmokeStart
	KindProjectileTrail = frame.KindProjectileTrail
	KindImpact          = frame.KindImpact
	KindWaterImpact     = frame.KindWaterImpact
	KindExplosion       = frame.KindExplosion
	KindLHTFlash        = frame.KindLHTFlash
	KindCorpse          = frame.KindCorpse
)

// EffectCapacity is the fixed active-effect pool bound [01 §6.1][03 §1].
const EffectCapacity = 300 // [03 §1] fixed active-effect pool capacity

// EffectPool is the sole mutable active-effect owner supplied by composition.
// The session admits detached event views and advances it; presentation only
// reads snapshots, never a second cursor or retirement clock [03 §1][I6].
type EffectPool interface {
	Len() int
	Update(uint32)
	AppendView(frame.EffectView) bool
	SnapshotViews() []frame.EffectView
	RemoveMatching(pool.Handle, pool.Handle, string)
	SnapshotViewsInto([]frame.EffectView) []frame.EffectView
	AdmitShatter(FragmentRequest, func(uint32) uint32) bool
	SetFragmentStepContext(FragmentStepContext)
	FragmentMetadataInto([]FragmentMetadata) []FragmentMetadata
}

// EffectService is an admission/snapshot adapter around the one canonical
// FixedEffectPool. It owns no second cursor, expiration, or retirement
// lifecycle; Advance updates that pool once, then appends this window's
// detached event views [03 §1][I6].
type EffectService struct {
	max          int
	owner        EffectPool
	pending      []frame.EffectView
	nextID       uint32
	lastSequence uint64
	dropped      uint64
	// refusedAtCapacity counts every admission refused because the pool was
	// full, from construction on, and is never reset. It is a host diagnostic:
	// no tick reads it, and it is neither fingerprinted nor saved.
	refusedAtCapacity uint64
	art               *content.SimArt
}

// newEffectService creates the effect admission adapter. max <= 0 uses
// the fixed pool capacity; a smaller value is retained only as a presentation
// admission bound for existing callers.
func newEffectService(max int) *EffectService {
	if max <= 0 {
		max = EffectCapacity
	}
	return &EffectService{max: max, nextID: 1}
}

// NewEffectServiceWithPool binds the canonical pool and immutable authored
// timing at composition, before any unit script can emit effects. A nil art
// table leaves named players inactive; timing is never guessed [03 §1][I9].
func NewEffectServiceWithPool(max int, owner EffectPool, art *content.SimArt) *EffectService {
	s := newEffectService(max)
	s.owner = owner
	s.art = art
	return s
}

// Dropped is the number of events the service has refused since its last
// Advance, which resets it, for diagnostics. Refusals by synchronous
// admissions earlier in a tick are cleared by that tick's Advance.
func (s *EffectService) Dropped() uint64 {
	if s == nil {
		return 0
	}
	return s.dropped
}

// RefusedAtCapacity is the number of admissions refused because the pool was
// full since the service was built, never reset: the service's own capacity
// test and the canonical pool declining the append, which FixedEffectPool
// does only at its capacity. Identity exhaustion and shatter quads (refused
// inside the pool's AdmitShatter) are not counted. Diagnostics and tests only;
// no tick reads it, and it is neither fingerprinted nor saved [03 §1].
func (s *EffectService) RefusedAtCapacity() uint64 {
	if s == nil {
		return 0
	}
	return s.refusedAtCapacity
}

// Occupancy reports the canonical pool's live records and the capacity
// admission compares them with, for diagnostics and tests. It reads only and
// changes nothing [03 §1].
func (s *EffectService) Occupancy() (live, capacity int) {
	if s == nil {
		return 0, 0
	}
	if s.owner != nil {
		return s.owner.Len(), s.max
	}
	return len(s.pending), s.max
}

// Advance updates the canonical pool once and admits the current ordered event
// window. Repeated nonzero sequences are ignored defensively.
func (s *EffectService) Advance(tick uint32, events []Event) {
	if s == nil {
		return
	}
	s.dropped = 0
	if s.owner != nil {
		s.owner.Update(tick)
	} else {
		s.pending = s.pending[:0]
	}
	for _, e := range events {
		if e.Sequence != 0 && e.Sequence <= s.lastSequence {
			continue
		}
		if e.Sequence != 0 {
			s.lastSequence = e.Sequence
		}
		if e.Kind == frame.KindSmokeEnd {
			s.removePending(e.Source, e.Target)
			if s.owner != nil {
				s.owner.RemoveMatching(e.Source, e.Target, KindSmokeStart.String())
			}
			continue
		}
		if !effectKind(e.Kind) {
			continue
		}
		s.Admit(tick, e)
	}
}

// Admit synchronously offers one effect to the canonical fixed pool. It is
// used by producers whose allocation happens inside an authoritative callback,
// where retaining a later event queue would change the shared pool's capacity
// decision [03 §1][I5].
func (s *EffectService) Admit(tick uint32, e Event) bool {
	if s == nil || !effectKind(e.Kind) {
		return false
	}
	return s.admit(tick, e)
}

// AdmitShatter synchronously forwards paired fragment admission to the sole
// fixed-effect owner. A refused quad consumes no fragment draws [04 R-COB-04 §3].
func (s *EffectService) AdmitShatter(req FragmentRequest, draw func(uint32) uint32) bool {
	if s == nil || s.owner == nil || draw == nil {
		return false
	}
	return s.owner.AdmitShatter(req, draw)
}

// SetFragmentStepContext installs the current terrain and water values for the
// fixed pool's in-loop fragment branch [04 R-COB-04 §3].
func (s *EffectService) SetFragmentStepContext(ctx FragmentStepContext) {
	if s == nil || s.owner == nil {
		return
	}
	s.owner.SetFragmentStepContext(ctx)
}

// FragmentMetadataInto reads the canonical paired geometry table for a later
// publication adapter. It owns no second geometry store [04 R-COB-04 §3][I6].
func (s *EffectService) FragmentMetadataInto(dst []FragmentMetadata) []FragmentMetadata {
	if s == nil || s.owner == nil {
		return dst[:0]
	}
	return s.owner.FragmentMetadataInto(dst)
}

// Snapshot returns a detached copy of the canonical pool's stable order.
func (s *EffectService) Snapshot() []frame.EffectView {
	if s == nil {
		return nil
	}
	return s.SnapshotInto(nil)
}

// SnapshotInto copies the active effect views into dst, retaining destination
// capacity and nested authored timing slices for steady-state publication.
func (s *EffectService) SnapshotInto(dst []frame.EffectView) []frame.EffectView {
	if s == nil {
		return dst[:0]
	}
	if s.owner != nil {
		return s.owner.SnapshotViewsInto(dst)
	}
	return copyEffectViews(dst, s.pending)
}

func copyEffectViews(dst, src []frame.EffectView) []frame.EffectView {
	if cap(dst) < len(src) {
		dst = make([]frame.EffectView, 0, len(src))
	}
	dst = dst[:0]
	for _, srcView := range src {
		n := len(dst)
		dst = dst[:n+1]
		a, b := dst[n].DurationsA, dst[n].DurationsB
		dst[n] = srcView
		dst[n].DurationsA = append(a[:0], srcView.DurationsA...)
		dst[n].DurationsB = append(b[:0], srcView.DurationsB...)
	}
	return dst
}

func (s *EffectService) admit(now uint32, e Event) bool {
	if s.owner != nil && s.owner.Len() >= s.max {
		s.noteDrop()
		s.noteCapacityRefusal()
		return false
	}
	id := e.ID
	if id == 0 {
		if s.nextID == 0 {
			s.noteDrop()
			return false
		}
		id = s.nextID
		s.nextID++
	} else if id >= s.nextID {
		s.nextID = id + 1
		if s.nextID == 0 {
			s.nextID = ^uint32(0)
		}
	}
	// A producer that admits here directly, such as a COB explosion or a
	// debris impact, is routed exactly as the event buffer routes one. Left
	// unset, its strip would read as barrier 0, which no retail producer
	// writes and which the composer draws beneath the features and both unit
	// passes; unrouted producers belong to the fixed pool [03 §1]
	// [03 R-STRIP-01 §3].
	frame.RouteEvent(&e)
	view := frame.EffectView{
		ID: id, EventSeq: e.Sequence, Source: e.Source, Target: e.Target,
		EffectID: e.EffectID, Piece: e.Piece, SFXType: e.SFXType,
		SFXClass: e.SFXClass, Mode: e.Mode, StartTick: e.Tick,
		Lifetime: e.Lifetime, X: e.X, Y: e.Y, Z: e.Z,
		TargetX: e.TargetX, TargetY: e.TargetY, TargetZ: e.TargetZ,
		Kind: e.Kind.String(), Graphic: e.Graphic, PaletteRow: e.PaletteRow,
		Light: e.Kind == KindLHTFlash, Shake: e.Magnitude,
		AssetID: e.AssetID, SequenceID: e.SequenceID,
		DurationsA: append([]int32(nil), e.DurationsA...),
		DurationsB: append([]int32(nil), e.DurationsB...),
		LoopA:      e.LoopA, LoopB: e.LoopB,
		FlashRadius: e.FlashRadius, FlashLevel: e.FlashLevel,
		HasFlashDisc: e.HasFlashDisc, Strip: e.Strip,
		HasCalculatedFlash: e.HasCalculatedFlash, CalculatedTable: e.CalculatedTable,
		HasBlastProfile: e.HasBlastProfile, BlastAreaOfEffect: e.BlastAreaOfEffect, BlastDamage: e.BlastDamage,
		NanolatheIndex: e.NanolatheIndex, NanolatheCount: e.NanolatheCount,
		NanolatheGeometryKnown:  e.NanolatheGeometryKnown,
		NanolatheTargetBoxKnown: e.NanolatheTargetBoxKnown,
		NanolatheTargetMin:      e.NanolatheTargetMin, NanolatheTargetMax: e.NanolatheTargetMax,
		NanolatheBoxAtSource: e.NanolatheBoxAtSource,
	}
	if e.ExpiryTick != 0 {
		view.ExpiryTick = e.ExpiryTick
	} else if e.Lifetime > 0 {
		view.ExpiryTick = e.Tick + uint32(e.Lifetime)
	}
	// The record's two players are independent [03 §1]: the PRIMARY is the
	// event's named art and the SECONDARY is whatever generated table the
	// producer attached, each with its own authored timing [06 R-WFX-01 §2].
	// The primary lookup is therefore gated on the PRIMARY lacking timing and
	// on nothing else.
	//
	// Corrected: this used to require that NEITHER player had timing. Every
	// impact publishes the calculated flash's holds as the secondary timing,
	// so the condition was never true for an impact and its named art was
	// admitted with no player at all — art that could only ever appear as a
	// static frame 0, for exactly as long as the flash kept the record alive.
	if e.Graphic != "" && !validDurations(view.DurationsA) {
		if holds, ok := s.art.EffectEntryHolds(e.AssetID, e.Graphic); ok {
			view.DurationsA = holds
			// Effect holders clear the authored loop byte [06 R-WFX-01 §1].
			view.LoopA = false
		}
	}
	if s.owner != nil {
		if !s.owner.AppendView(view) {
			// The canonical pool declines an append only at its capacity.
			s.noteDrop()
			s.noteCapacityRefusal()
			return false
		}
	} else {
		// The pool is the normal publisher of per-player liveness and this
		// fixture fallback bypasses it, so mirror its admission rule here — a
		// player is live when its authored timing resolved — rather than
		// leaving pending views silently undrawable [03 §1][I9].
		view.ActiveA = validDurations(view.DurationsA)
		view.ActiveB = validDurations(view.DurationsB)
		s.pending = append(s.pending, view)
	}
	_ = now // the pool advances once per call; StartTick remains event-authored.
	return true
}

func (s *EffectService) removePending(source, target pool.Handle) {
	write := 0
	removed := false
	for _, view := range s.pending {
		if !removed && view.Kind == KindSmokeStart.String() && sameEndpoint(view.Source, source, view.Target, target) {
			removed = true
			continue
		}
		s.pending[write] = view
		write++
	}
	s.pending = s.pending[:write]
}

func (s *EffectService) noteDrop() {
	if s.dropped != ^uint64(0) {
		s.dropped++
	}
}

func (s *EffectService) noteCapacityRefusal() {
	if s.refusedAtCapacity != ^uint64(0) {
		s.refusedAtCapacity++
	}
}

func effectKind(k Kind) bool {
	switch k {
	case KindCOBSFX, KindNanolathe, KindMuzzleFlash, KindSmokeStart,
		KindProjectileTrail, KindImpact, KindWaterImpact, KindExplosion,
		KindLHTFlash, KindCorpse:
		return true
	default:
		return false
	}
}

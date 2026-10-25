package session

import (
	"fmt"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

// SurvivalSaveContentDigest compares a resumed Survival attempt with the
// content its original best-score key used (DESIGN_SURVIVAL §8.1, §11).
// Retail restoration reads live sources, so it has no retained frozen-input
// digest. Capture those sources under the restored selection to compute the
// same identity without composing a second battle or changing the session.
func (s *Session) SurvivalSaveContentDigest(fs vfs.FSOps) ([32]byte, error) {
	if digest, ok := s.SimulationContentDigest(); ok {
		return digest, nil
	}
	inputs, err := s.captureSurvivalSaveContent(fs)
	if err != nil {
		return [32]byte{}, err
	}
	return inputs.Digest(), nil
}

func (s *Session) captureSurvivalSaveContent(fs vfs.FSOps) (*content.SimulationInputs, error) {
	if s == nil || !s.IsSurvival() || s.Mission == nil || s.Catalog == nil || fs == nil {
		return nil, fmt.Errorf("session: Survival save content identity requires a restored Survival battle and its sources")
	}
	// Restoring a bank marks definition-side weapon active bytes as restored.
	// Those are saved runtime values, not the authored content identity that
	// the original attempt was admitted under. Reuse entry's capture and
	// preparation with fresh definitions instead of hashing the restored clone.
	cfg := s.Skirmish
	cfg.Gameplay = s.Gameplay
	entry, err := prepareSkirmishInputs(fs, nil, cfg, SkirmishEntryOptions{
		CommunitySources: s.CommunitySources, ContentLimits: s.Catalog.Limits,
		Mutators: s.Mutators, Restrictions: s.Restrictions,
	}, false)
	if err != nil {
		return nil, err
	}
	return entry.inputs, nil
}

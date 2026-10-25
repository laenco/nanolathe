package replay

import "github.com/nanolathe-gg/nanolathe/internal/session"

// Configuration decodes the battle configuration in its recording's format,
// preserving the original bytes and identity. V1 files precede room content;
// v2 freezes that field before its extension tail (DESIGN_MULTIPLAYER §10).
func (h Header) Configuration() (session.EffectiveMatchConfig, error) {
	return session.DecodeReplayMatchConfig(h.Config, h.FileVersion())
}

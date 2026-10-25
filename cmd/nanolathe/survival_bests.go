package main

// Personal bests stay on the host: they neither enter a committed frame nor
// influence a battle (docs/DESIGN_SURVIVAL.md §8.1).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/frame"
	"github.com/nanolathe-gg/nanolathe/internal/headless"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/settings"
)

type survivalBestRun struct {
	key      string
	content  [32]byte
	best     settings.SurvivalBest
	exists   bool
	improved bool
	finished bool
	excluded string
	err      error
}

// survivalBestKey uses the resolved battle configuration and actual simulation
// content. Only repeat-attempt and cosmetic identity is removed. The version
// separates future changes to the scoring or comparison policy.
func survivalBestKey(r session.MatchConfigRequest, digest [32]byte) (string, error) {
	r.Seats = slices.Clone(r.Seats)
	r.SimulationSeed, r.CRTSeed = 0, 0
	r.MapName = content.CanonicalKey(r.MapName)
	mutators := r.Mutators.String()
	r.Mutators = content.Mutators{}
	for i := range r.Seats {
		r.Seats[i].Nickname = ""
		r.Seats[i].Color = 0
		r.Seats[i].Participant = session.MatchParticipantID{}
	}
	data, err := json.Marshal(struct {
		Version  int
		Content  [32]byte
		Setup    session.MatchConfigRequest
		Mutators string
	}{1, digest, r, mutators})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func newSurvivalBestRun(request headless.FreshBattleRequest, cs *contentSet, s *session.Session) *survivalBestRun {
	if request.Kind != headless.ScenarioSurvival || request.Watching || request.AutomatedPlayers || cs == nil || s == nil || !s.IsSurvival() || s.OnlineCommandContext() {
		return nil
	}
	run := &survivalBestRun{}
	digest, ok := s.SimulationContentDigest()
	if !ok {
		run.excluded = "content identity unavailable"
		return run
	}
	run.content = digest
	// Reuse the local setup adapter, without online admission's seat limits:
	// Strict single-player Survival permits two buddies. The frozen content
	// digest already includes the applied restrictions and selected map schema,
	// including removed definitions; no catalog or map is compiled again here.
	cfg := s.Skirmish
	cfg.Gameplay = request.Gameplay
	config, err := session.NewMatchConfigRequest(cfg, session.SkirmishEntryOptions{
		BuilderOptions: request.BuilderOptions, CommunitySources: request.CommunitySources,
		ContentLimits: s.Catalog.Limits, Mutators: request.Mutators, AIOverrides: request.AIOverrides,
	}, session.MatchRoomInputs{Mod: matchModOf(cs.mod), ContentProfile: cs.profile})
	if err == nil {
		run.key, err = survivalBestKey(config, digest)
	}
	if err == nil {
		run.best, run.exists, err = settings.LoadSurvivalBest(run.key)
	}
	run.err = err
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	return run
}

// noteSurvivalCommand is called only for accepted host submissions. The flag
// is sticky, so restoring fog, rules or resources cannot make a run eligible.
func (b *battleSession) noteSurvivalCommand(c session.HumanCommand) {
	if b.survivalBest == nil {
		return
	}
	switch c.Kind {
	case session.HumanATM, session.HumanSetResource, session.HumanView,
		session.HumanVisibility, session.HumanDoubleShot, session.HumanHalfShot,
		session.HumanMeteor, session.HumanMakeSelectable,
		session.HumanSpawn, session.HumanDeveloperSpawn:
		b.survivalBest.excluded = "cheats used"
	case session.HumanGameplay:
		b.survivalBest.excluded = "rules changed during battle"
	case session.HumanGive:
		if c.Give.Amount < 0 {
			b.survivalBest.excluded = "cheats used"
		}
	}
}

// enqueueLocalBattleCommand preserves the local command path while noting
// best-score eligibility outside the session and its RNG/state identity.
func (b *battleSession) enqueueLocalBattleCommand(c session.HumanCommand) error {
	err := b.sess.EnqueueHumanCommand(c)
	if err == nil {
		b.noteSurvivalCommand(c)
	}
	return err
}

func (b *battleSession) finishSurvivalBest(view frame.ResultView) {
	run := b.survivalBest
	if run == nil || run.finished || !view.Ended || view.Survival == nil {
		return
	}
	run.finished = true
	if b.onlineBattle() || b.playback != nil || b.preview || run.excluded != "" || run.err != nil || run.key == "" {
		return
	}
	if view.Kind != "defeat" {
		run.excluded = "battle left before defeat"
		return
	}
	r := view.Survival
	run.best, run.improved, run.err = settings.StoreSurvivalBest(run.key, settings.SurvivalBest{Score: r.Score, Waves: r.Waves, TicksAlive: r.TicksAlive})
	if run.err != nil {
		fmt.Fprintln(os.Stderr, run.err)
		return
	}
	run.exists = true
}

func (b *battleSession) survivalBestResultLine() string {
	if b == nil {
		return ""
	}
	if b.playback != nil {
		return "Personal best not recorded (replay)"
	}
	if b.onlineBattle() {
		return "Personal best not recorded (online game)"
	}
	run := b.survivalBest
	if run == nil {
		return ""
	}
	if run.excluded != "" {
		return "Personal best not recorded (" + run.excluded + ")"
	}
	if run.err != nil {
		return "Personal best unavailable - records could not be read or saved"
	}
	if !run.exists {
		return "No personal best for this setup yet"
	}
	label := "Personal best for this setup: "
	if run.improved {
		label = "New personal best for this setup: "
	}
	return label + groupDigits(run.best.Score)
}

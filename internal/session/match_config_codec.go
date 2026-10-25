package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/nanolathe-gg/nanolathe/internal/ai"
	"github.com/nanolathe-gg/nanolathe/internal/netproto"
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/survival"
)

// The wire form of configuration version 2 (docs/DESIGN_MULTIPLAYER.md §8.6)
// in the primitives of §7.4.1, written and read through internal/netproto: u8
// and bool are one byte, a bool exactly 0 or 1; u16, u32 and u64 are the
// shortest unsigned base-128 varint within their width; s32 is the zigzag of
// the 32-bit value, then a varint; text(N) is a u32 byte length and at most N
// bytes of UTF-8 without NUL; key is a u16 byte length and 1..255 bytes of a
// canonical content key; digest is 32 raw bytes and id 16. Collections carry
// a u32 count. Fields 1–16 are frozen. An optional nonempty extension tail
// follows field 16; its zero ID terminator is the last byte (§8.6).
//
// Design reading: the payload carries no version of its own. Version 2 is
// named by the identity domain and by the envelope that carries the payload,
// as the command schema version is negotiated outside a command payload
// (§7.4.1).

// matchConfigDomain is the literal UTF-8 domain the identity hashes before
// the encoding, with no trailing NUL.
const (
	matchConfigDomain       = "nanolathe/match-config/2"
	matchConfigLegacyDomain = "nanolathe/match-config/1"
)

// EncodeMatchConfig returns the canonical encoding of a resolved
// configuration: a fresh copy of the bytes it was frozen with. The zero value
// is no configuration and has no encoding.
func EncodeMatchConfig(c EffectiveMatchConfig) ([]byte, error) {
	if c.encoding == nil {
		return nil, matchFieldError("configuration", "a configuration resolution or decoding produced")
	}
	return append([]byte(nil), c.encoding...), nil
}

// DecodeMatchConfig reads a version 2 payload. It refuses a payload over 2
// MiB, a count or length over its bound before allocating from it, an
// overlong or overflowing integer, a boolean other than 0 or 1, any field
// ResolveMatchConfig refuses, trailing bytes, and any encoding that is not
// the one the decoded value re-encodes to. It never fills a default and
// returns no partially valid value.
func DecodeMatchConfig(payload []byte) (EffectiveMatchConfig, error) {
	return decodeMatchConfig(payload, 2)
}

// DecodeReplayMatchConfig reads a historical or current replay configuration.
// Version 1 contains only fields 1–15 and keeps its original encoding and
// identity domain; version 2 is the current schema (§8.6, §10). This does not
// admit legacy configurations to the current online protocol.
func DecodeReplayMatchConfig(payload []byte, version uint16) (EffectiveMatchConfig, error) {
	if version != 1 && version != 2 {
		return EffectiveMatchConfig{}, matchFieldError("configuration.version", fmt.Sprintf("supported replay configuration version 1 or 2, got %d", version))
	}
	return decodeMatchConfig(payload, version)
}

func decodeMatchConfig(payload []byte, version uint16) (EffectiveMatchConfig, error) {
	if len(payload) > matchConfigMaxBytes {
		return EffectiveMatchConfig{}, matchFieldError("payload", fmt.Sprintf("at most %d bytes, got %d", matchConfigMaxBytes, len(payload)))
	}
	r := netproto.NewReader(payload, matchFieldError)
	request := readMatchRequest(r, version)
	if version == 2 && r.Err() == nil && r.Offset() < len(payload) {
		request.Extensions = readMatchExtensions(r)
	}
	if err := r.End(); err != nil {
		return EffectiveMatchConfig{}, err
	}
	c, err := resolveMatchConfig(request, version)
	if err != nil {
		return EffectiveMatchConfig{}, err
	}
	if !bytes.Equal(c.encoding, payload) {
		return EffectiveMatchConfig{}, matchFieldError("payload", "the canonical encoding of the value it decodes to")
	}
	return c, nil
}

// Digest is the configuration identity: SHA-256 of the domain and the
// canonical encoding. It cannot fail, because a value exists only after
// resolution validated it; the zero value, no configuration, digests to
// zero.
func (c EffectiveMatchConfig) Digest() [32]byte {
	if c.encoding == nil {
		return [32]byte{}
	}
	h := sha256.New()
	domain := matchConfigDomain
	if c.version == 1 {
		domain = matchConfigLegacyDomain
	}
	h.Write([]byte(domain))
	h.Write(c.encoding)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// encodeMatchRequest writes a request validate has accepted, in the shared
// primitives (internal/netproto). Version 1 stops after field 15.
func encodeMatchRequest(r *MatchConfigRequest, version uint16) ([]byte, error) {
	var w netproto.Writer
	w.U8(uint8(r.SessionKind)) // 1
	w.Key(r.RuleName)          // 2
	w.U8(uint8(r.RuleBase))
	w.Text(r.MapName) // 3
	w.U32(r.MapSchema)
	w.U8(uint8(len(r.Seats))) // 4
	for i := range r.Seats {
		writeMatchSeat(&w, &r.Seats[i])
	}
	w.U8(r.Location) // 5
	w.U8(r.CommanderDeath)
	w.U8(r.Mapping)
	w.U8(r.LineOfSight)
	w.U8(r.LOSType)
	w.U16(r.UnitLimit) // 6
	w.U32(r.SimulationSeed)
	w.U32(r.CRTSeed)
	w.U8(uint8(r.SurvivalPace)) // 7
	w.Bool(r.SurvivalNoAir)
	w.Bool(r.SurvivalNoNaval)
	w.Text(r.Mod.ID) // 8
	w.Text(r.Mod.Version)
	w.Digest(r.Mod.Archive)
	p := &r.ContentProfile // 9
	w.Text(p.Name)
	w.U32(uint32(len(p.Directories)))
	for _, d := range p.Directories {
		w.Text(d.From)
		w.Text(d.To)
	}
	w.U32(p.Units)
	w.U32(p.Weapons)
	w.U64(p.TNTBytes)
	w.U64(p.LOSBytes)
	community, err := json.Marshal(r.Community) // 10, validated by validate
	if err != nil {
		return nil, matchFieldError("community", "a table with a JSON form: "+err.Error())
	}
	w.U32(uint32(len(community)))
	w.Raw(community)
	for _, f := range matchMutatorFields(&r.Mutators) { // 11
		index, _ := matchMutatorIndex(*f)
		w.U8(index)
	}
	w.U32(uint32(len(r.UnitRestrictions))) // 12
	for _, u := range r.UnitRestrictions {
		w.U16(u.DefinitionID)
		w.Key(u.Unit)
		w.U8(u.Limit)
	}
	w.Bool(r.CheatsAllowed) // 13
	w.Bool(r.WatchingAllowed)
	for _, v := range []MatchView{r.PlayerView, r.SpectatorView, r.ReplayView} { // 14
		w.U16(v.MinimumScale)
		w.U16(v.MaximumScale)
		w.Bool(v.FullMap)
	}
	pol := r.Policies // 15
	w.U16(pol.Revision)
	w.U8(pol.Scheduling)
	w.U8(pol.Pacing)
	w.U8(pol.Drop)
	w.U8(pol.Audience)
	w.U32(pol.RejoinGraceMilliseconds)
	w.U32(pol.SpectatorDelayMilliseconds)
	w.U32(pol.ReplayReleaseDelayMilliseconds)
	if version == 1 {
		return w.Bytes(), nil
	}
	w.U32(uint32(len(r.RoomContent))) // 16
	for _, e := range r.RoomContent {
		w.U8(uint8(e.Kind))
		w.Text(e.Name)
		w.Text(e.Version)
		w.U64(e.Size)
		w.Digest(e.Digest)
	}
	extensions, err := r.setupExtensions()
	if err != nil {
		return nil, err
	}
	writeMatchExtensions(&w, extensions)
	return w.Bytes(), nil
}

func writeMatchSeat(w *netproto.Writer, s *MatchSeat) {
	w.U8(uint8(s.Role))
	w.U8(s.Side)
	w.U8(s.Color)
	w.U8(s.AllyGroup)
	w.Text(s.Nickname)
	w.S32(s.Metal)
	w.S32(s.Energy)
	w.ID(s.Participant)
	w.U8(s.HostSeat)
	w.U8(uint8(s.ComputerKind))
	w.U8(s.Difficulty)
	w.Bool(s.SharedVictory)
	for _, g := range s.BuilderOptions.Guard {
		w.U8(uint8(g))
	}
	for _, p := range s.BuilderOptions.Patrol {
		w.U8(uint8(p))
	}
	w.U32(uint32(len(s.AIParams)))
	for _, p := range s.AIParams {
		w.Text(p.Key)
		w.Text(p.Value)
	}
}

// matchUvarintLen is the length of v's shortest varint, for validate's
// encoded-size bound on a row's parameters.
func matchUvarintLen(v uint64) int { return netproto.UvarintLen(v) }

// readMatchRequest reads the fields of §8.6 in order. A text field's UTF-8
// and NUL rule is the reader's; every other content rule is validate's, which
// DecodeMatchConfig runs on the result.
func readMatchRequest(r *netproto.Reader, version uint16) MatchConfigRequest {
	var q MatchConfigRequest
	q.SessionKind = MatchSessionKind(r.U8()) // 1
	q.RuleName = r.Key()                     // 2
	q.RuleBase = MatchRuleBase(r.U8())
	q.MapName = r.Text(matchMapNameMaxBytes) // 3
	q.MapSchema = r.U32()
	seats := r.U8() // 4
	if r.Err() == nil && (seats < matchMinSeats || seats > SkirmishMaxPlayers) {
		r.FailAt(r.Offset()-1, fmt.Sprintf("%d..%d seats", matchMinSeats, SkirmishMaxPlayers))
	}
	if r.Err() != nil {
		return MatchConfigRequest{}
	}
	q.Seats = make([]MatchSeat, seats)
	for i := range q.Seats {
		q.Seats[i] = readMatchSeat(r)
	}
	q.Location = r.U8() // 5
	q.CommanderDeath = r.U8()
	q.Mapping = r.U8()
	q.LineOfSight = r.U8()
	q.LOSType = r.U8()
	q.UnitLimit = r.U16() // 6
	q.SimulationSeed = r.U32()
	q.CRTSeed = r.U32()
	q.SurvivalPace = survival.Pace(r.U8()) // 7
	q.SurvivalNoAir = r.Bool()
	q.SurvivalNoNaval = r.Bool()
	q.Mod.ID = r.Text(matchModTextMaxBytes) // 8
	q.Mod.Version = r.Text(matchModTextMaxBytes)
	q.Mod.Archive = r.Digest()
	p := &q.ContentProfile // 9
	p.Name = r.Text(matchProfileNameMaxBytes)
	if n := r.Count(matchMaxDirectories, 2); n > 0 {
		p.Directories = make([]MatchDirectory, n)
		for i := range p.Directories {
			p.Directories[i].From = r.Text(matchDirFromMaxBytes)
			p.Directories[i].To = r.Text(matchDirToMaxBytes)
		}
	}
	p.Units = r.U32()
	p.Weapons = r.U32()
	p.TNTBytes = r.U64()
	p.LOSBytes = r.U64()
	community := r.U32() // 10
	if r.Err() == nil && community > matchCommunityMaxBytes {
		r.Fail(fmt.Sprintf("community JSON of at most %d bytes", matchCommunityMaxBytes))
	}
	data := r.Raw(int(community))
	if r.Err() == nil {
		f, err := decodeMatchCommunity(data)
		if err != nil {
			r.Abort(err)
		}
		q.Community = f
	}
	for _, f := range matchMutatorFields(&q.Mutators) { // 11
		index := r.U8()
		factor, ok := matchMutatorFactor(index)
		if r.Err() == nil && !ok {
			r.FailAt(r.Offset()-1, "a mutator step index 0..7")
		}
		*f = factor
	}
	if n := r.Count(matchMaxRestrictions, 4); n > 0 { // 12
		q.UnitRestrictions = make([]MatchUnitRestriction, n)
		for i := range q.UnitRestrictions {
			u := &q.UnitRestrictions[i]
			u.DefinitionID = r.U16()
			u.Unit = r.Key()
			u.Limit = r.U8()
		}
	}
	q.CheatsAllowed = r.Bool() // 13
	q.WatchingAllowed = r.Bool()
	for _, v := range []*MatchView{&q.PlayerView, &q.SpectatorView, &q.ReplayView} { // 14
		v.MinimumScale = r.U16()
		v.MaximumScale = r.U16()
		v.FullMap = r.Bool()
	}
	pol := &q.Policies // 15
	pol.Revision = r.U16()
	pol.Scheduling = r.U8()
	pol.Pacing = r.U8()
	pol.Drop = r.U8()
	pol.Audience = r.U8()
	pol.RejoinGraceMilliseconds = r.U32()
	pol.SpectatorDelayMilliseconds = r.U32()
	pol.ReplayReleaseDelayMilliseconds = r.U32()
	if version == 1 {
		return q
	}
	if n := r.Count(matchMaxRoomContent, 36); n > 0 { // 16
		q.RoomContent = make([]MatchContentEntry, n)
		for i := range q.RoomContent {
			e := &q.RoomContent[i]
			e.Kind = MatchContentKind(r.U8())
			e.Name = r.Text(matchContentNameMaxBytes)
			e.Version = r.Text(matchContentVersionBytes)
			e.Size = r.U64()
			e.Digest = r.Digest()
		}
	}
	if r.Err() != nil {
		return MatchConfigRequest{}
	}
	return q
}

func readMatchSeat(r *netproto.Reader) MatchSeat {
	var s MatchSeat
	s.Role = MatchRole(r.U8())
	s.Side = r.U8()
	s.Color = r.U8()
	s.AllyGroup = r.U8()
	s.Nickname = r.Text(matchNicknameMaxBytes)
	s.Metal = r.S32()
	s.Energy = r.S32()
	s.Participant = r.ID()
	s.HostSeat = r.U8()
	s.ComputerKind = ai.Controller(r.U8())
	s.Difficulty = r.U8()
	s.SharedVictory = r.Bool()
	for i := range s.BuilderOptions.Guard {
		s.BuilderOptions.Guard[i] = orders.GuardHomeOption(r.U8())
	}
	for i := range s.BuilderOptions.Patrol {
		s.BuilderOptions.Patrol[i] = orders.PatrolWorkOption(r.U8())
	}
	if n := r.Count(matchMaxAIParams, 2); n > 0 {
		s.AIParams = make([]AIParam, n)
		for i := range s.AIParams {
			s.AIParams[i].Key = r.Text(matchAIParamMaxBytes)
			s.AIParams[i].Value = r.Text(matchAIParamMaxBytes)
		}
	}
	return s
}

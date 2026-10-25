package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func encodeMatchForTest(t testing.TB, c EffectiveMatchConfig) []byte {
	t.Helper()
	payload, err := EncodeMatchConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

// checkMatchDecode is the codec's one property: a payload either fails to
// decode, or decodes to a value whose encoding is that payload exactly and
// whose identity is stable through another resolution. A decoder never
// accepts one value and hands back another.
func checkMatchDecode(t testing.TB, payload []byte) bool {
	t.Helper()
	c, err := DecodeMatchConfig(payload)
	if err != nil {
		if c.Digest() != ([32]byte{}) {
			t.Fatalf("a refused payload returned a value: %v", err)
		}
		return false
	}
	if again := encodeMatchForTest(t, c); !bytes.Equal(again, payload) {
		t.Fatalf("an accepted payload re-encodes differently:\n got %x\nwant %x", again, payload)
	}
	if r, err := ResolveMatchConfig(c.Request()); err != nil || r.Digest() != c.Digest() {
		t.Fatalf("an accepted payload's value does not resolve to itself: %v", err)
	}
	return true
}

// Without extensions, every proper prefix is refused, and a single
// trailing byte cannot form a valid extension tail.
func TestDecodeMatchConfigRefusesTruncationAndTrailingBytes(t *testing.T) {
	for _, r := range []MatchConfigRequest{validMatchRequest(t), validSurvivalRequest(t)} {
		payload := encodeMatchForTest(t, resolveMatch(t, r))
		for n := 0; n < len(payload); n++ {
			if checkMatchDecode(t, payload[:n]) {
				t.Fatalf("a %d-byte prefix of a %d-byte payload decoded", n, len(payload))
			}
		}
		for _, tail := range [][]byte{{0}, {1}, {0xff}} {
			if checkMatchDecode(t, append(append([]byte(nil), payload...), tail...)) {
				t.Fatalf("a payload with trailing byte %x decoded", tail)
			}
		}
	}
}

// Flipping any bit pattern of any byte of a valid payload is either refused
// or a different, exactly re-encoding configuration. Some flips are both
// valid and different — a seed, a nickname letter — and those must change
// the identity.
func TestDecodeMatchConfigByteMutations(t *testing.T) {
	for _, r := range []MatchConfigRequest{validMatchRequest(t), validSurvivalRequest(t)} {
		c := resolveMatch(t, r)
		payload := encodeMatchForTest(t, c)
		accepted := 0
		for i := range payload {
			for _, mask := range []byte{0x01, 0x02, 0x04, 0x40, 0x80, 0xff} {
				mutated := append([]byte(nil), payload...)
				mutated[i] ^= mask
				if checkMatchDecode(t, mutated) {
					accepted++
					if d, _ := DecodeMatchConfig(mutated); d.Digest() == c.Digest() {
						t.Fatalf("byte %d ^ %#x decoded to the original identity", i, mask)
					}
				}
			}
		}
		if accepted == 0 {
			t.Fatal("no mutation was accepted: the sweep cannot tell a strict decoder from a broken one")
		}
	}
}

// Bounded random edits — replaced, deleted and inserted bytes and cut tails,
// several to a payload — hold the same property. The generator is a fixed
// xorshift so every run edits the same way.
func TestDecodeMatchConfigRandomEdits(t *testing.T) {
	seeds := [][]byte{
		encodeMatchForTest(t, resolveMatch(t, validMatchRequest(t))),
		encodeMatchForTest(t, resolveMatch(t, validSurvivalRequest(t))),
	}
	state := uint64(0x9e3779b97f4a7c15)
	next := func(n int) int {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		return int(state % uint64(n))
	}
	for i := 0; i < 20000; i++ {
		p := append([]byte(nil), seeds[i%len(seeds)]...)
		for k := 1 + next(4); k > 0 && len(p) > 0; k-- {
			at := next(len(p))
			switch next(4) {
			case 0:
				p[at] = byte(next(256))
			case 1:
				p = append(p[:at], p[at+1:]...)
			case 2:
				p = append(p[:at], append([]byte{byte(next(256))}, p[at:]...)...)
			case 3:
				p = p[:at]
			}
		}
		checkMatchDecode(t, p)
	}
}

// splice replaces payload[at:at+n] with with.
func splice(payload []byte, at, n int, with ...byte) []byte {
	out := append([]byte(nil), payload[:at]...)
	out = append(out, with...)
	return append(out, payload[at+n:]...)
}

// uvarintBytes is a test-side varint, written independently of the codec.
func uvarintBytes(v uint64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v == 0 {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

// Structural attacks on known fields: counts over their bound or over the
// bytes that remain, an out-of-range enumeration and overlong integers are
// refused before anything is built from them.
func TestDecodeMatchConfigRefusesHostileFields(t *testing.T) {
	payload := encodeMatchForTest(t, resolveMatch(t, validMatchRequest(t)))
	find := func(pattern []byte) int {
		t.Helper()
		at := bytes.Index(payload, pattern)
		if at < 0 || bytes.Contains(payload[at+1:], pattern) {
			t.Fatalf("pattern %x is not unique in the payload", pattern)
		}
		return at
	}
	// Field 3 ends with the schema, a varint 0, and the seat count follows.
	mapEnd := find([]byte("Great Divide")) + len("Great Divide")
	if payload[mapEnd] != 0 || payload[mapEnd+1] != 3 {
		t.Fatalf("unexpected layout after the map name: %x", payload[mapEnd:mapEnd+2])
	}
	// The table's JSON is between 128 and 16383 bytes, so its length is a
	// two-byte varint just before it.
	table, _ := matchCommunityJSONForTest(matchTestCommunity(t))
	communityLength := find(table) - 2
	if !bytes.Equal(payload[communityLength:communityLength+2], uvarintBytes(uint64(len(table)))) {
		t.Fatalf("unexpected community length bytes %x", payload[communityLength:communityLength+2])
	}
	mutators := find(bytes.Repeat([]byte{3}, 11))
	params := find(append([]byte{2, 6}, "jitter"...))
	directories := find(append([]byte{2, 8}, "gamedata"...))
	restrictions := mutators + 11
	if payload[restrictions] != 2 {
		t.Fatalf("unexpected restriction count %d", payload[restrictions])
	}

	cases := []struct {
		name    string
		payload []byte
	}{
		{"overlong schema", splice(payload, mapEnd, 1, 0x80, 0x00)},
		{"no seats", splice(payload, mapEnd+1, 1, 0)},
		{"one seat", splice(payload, mapEnd+1, 1, 1)},
		{"eleven seats", splice(payload, mapEnd+1, 1, 11)},
		{"255 seats", splice(payload, mapEnd+1, 1, 255)},
		{"community over 16 KiB", splice(payload, communityLength, 2, uvarintBytes(16<<10+1)...)},
		{"community over the payload", splice(payload, communityLength, 2, uvarintBytes(uint64(len(payload)))...)},
		{"community length overflow", splice(payload, communityLength, 2, 0xff, 0xff, 0xff, 0xff, 0x7f)},
		{"mutator index 8", splice(payload, mutators, 1, 8)},
		{"mutator index 255", splice(payload, mutators+10, 1, 255)},
		{"129 AI parameters", splice(payload, params, 1, uvarintBytes(129)...)},
		{"128 AI parameters read from the following fields", splice(payload, params, 1, uvarintBytes(128)...)},
		{"65 directories", splice(payload, directories, 1, 65)},
		{"restrictions over the bound", splice(payload, restrictions, 1, uvarintBytes(65536)...)},
		{"restrictions over the payload", splice(payload, restrictions, 1, uvarintBytes(65535)...)},
		{"overlong restriction count", splice(payload, restrictions, 1, 0x82, 0x00)},
	}
	for _, c := range cases {
		if checkMatchDecode(t, c.payload) {
			t.Errorf("%s: decoded", c.name)
		}
	}
	if checkMatchDecode(t, make([]byte, 2<<20+1)) {
		t.Fatal("a payload over 2 MiB decoded")
	}
}

// The configuration encodes through the shared primitives of
// internal/netproto (whose own tests hold the primitive vectors this file
// once held for its private reader) byte for byte as it did with its own
// writer. version1 is the SHA-256 of the version-1 encoding recorded before
// the primitives moved; version 2 is those bytes followed by field 16's
// count, one zero byte when the room names no content, and its identity is
// recorded under the version-2 domain (§8.6).
func TestMatchConfigEncodingIsUnchangedBySharedPrimitives(t *testing.T) {
	for _, c := range []struct {
		name     string
		request  MatchConfigRequest
		length   int
		version1 string
		digest   string
	}{
		{"skirmish", validMatchRequest(t), 1149, "3a7a322d0faed979d9b29735ef3b2f31a9d85e2fa315f7fd823252ff005aedd7", "d772688c2bb9173e0b585d24fff11e31e75805076f3979bde585e40b3fa55e4d"},
		{"Survival", validSurvivalRequest(t), 1183, "a468914062cd58c07cf9042d340cbd31e43dd4bdee7030827eec98ff2c001751", "4b060da555e50888b705d5794ad6490b728e68e8d0ca70e243c8dfd8ce1e5d22"},
	} {
		config := resolveMatch(t, c.request)
		payload := encodeMatchForTest(t, config)
		digest := config.Digest()
		if len(payload) != c.length || payload[len(payload)-1] != 0 {
			t.Errorf("%s: %d bytes ending %#x; want %d ending in field 16's empty count", c.name, len(payload), payload[len(payload)-1], c.length)
			continue
		}
		sum := sha256.Sum256(payload[:len(payload)-1])
		if hex.EncodeToString(sum[:]) != c.version1 || hex.EncodeToString(digest[:]) != c.digest {
			t.Errorf("%s: version-1 part %x, identity %x; want the recorded %s, %s", c.name, sum, digest, c.version1, c.digest)
		}
		// The recorded version-1 vector is the complete historical payload,
		// not a current configuration with an empty field 16 appended.
		legacyPayload := append([]byte(nil), payload[:len(payload)-1]...)
		legacy, err := DecodeReplayMatchConfig(legacyPayload, 1)
		if err != nil {
			t.Fatalf("%s: legacy replay configuration: %v", c.name, err)
		}
		wantLegacy := sha256.Sum256(append([]byte("nanolathe/match-config/1"), legacyPayload...))
		if legacy.Digest() != wantLegacy || !bytes.Equal(encodeMatchForTest(t, legacy), legacyPayload) {
			t.Fatalf("%s: legacy encoding or identity changed", c.name)
		}
		if legacy.Digest() == digest || !reflect.DeepEqual(legacy.Request(), config.Request()) {
			t.Fatalf("%s: legacy fields or versioned identity lost", c.name)
		}
		if request := legacy.Request(); request.RoomContent != nil || request.Extensions != nil {
			t.Fatalf("%s: legacy decoding filled fields absent from version 1", c.name)
		}
		// All public storage boundaries own their bytes, including legacy
		// decode input and EncodeMatchConfig's returned copy.
		legacyPayload[0] ^= 0xff
		if legacy.Digest() != wantLegacy || bytes.Equal(encodeMatchForTest(t, legacy), legacyPayload) {
			t.Fatalf("%s: legacy decoding retained the input buffer", c.name)
		}
		encoded := encodeMatchForTest(t, legacy)
		encoded[0] ^= 0xff
		if bytes.Equal(encoded, encodeMatchForTest(t, legacy)) {
			t.Fatalf("%s: legacy encoding exposed frozen bytes", c.name)
		}
		legacyPayload = payload[:len(payload)-1]
		if _, err := DecodeMatchConfig(legacyPayload); err == nil {
			t.Fatalf("%s: current online decoding admitted version 1", c.name)
		}
		for _, invalid := range [][]byte{legacyPayload[:len(legacyPayload)-1], payload, append(append([]byte(nil), legacyPayload...), 1)} {
			if bad, err := DecodeReplayMatchConfig(invalid, 1); err == nil || bad.Digest() != ([32]byte{}) {
				t.Fatalf("%s: malformed legacy payload accepted: %v", c.name, err)
			}
		}
		current, err := DecodeReplayMatchConfig(payload, 2)
		if err != nil || current.Digest() != digest {
			t.Fatalf("%s: version-2 replay configuration changed: %v", c.name, err)
		}
		current.version = 0
		if current.Digest() != digest {
			t.Fatalf("%s: default version metadata changed the current identity", c.name)
		}
	}
}

// The whole configuration is at most 2 MiB: a request whose encoding would
// be larger does not resolve.
func TestMatchConfigRefusesAnOversizedEncoding(t *testing.T) {
	r := validMatchRequest(t)
	r.UnitRestrictions = nil
	key := strings.Repeat("u", 250)
	for id := 1; id <= 9000; id++ {
		r.UnitRestrictions = append(r.UnitRestrictions, MatchUnitRestriction{DefinitionID: uint16(id), Unit: key + strconv.Itoa(id%10000), Limit: 1})
	}
	if _, err := ResolveMatchConfig(r); err == nil || !strings.Contains(err.Error(), "encoding of at most") {
		t.Fatalf("a configuration over 2 MiB resolved: %v", err)
	}
}

// FuzzDecodeMatchConfig holds the codec property over arbitrary input,
// seeded with the two valid payloads. The fast tier runs the seeds; `go test
// -fuzz` explores further.
func FuzzDecodeMatchConfig(f *testing.F) {
	f.Add(encodeMatchForTest(f, resolveMatch(f, validMatchRequest(f))))
	f.Add(encodeMatchForTest(f, resolveMatch(f, validSurvivalRequest(f))))
	f.Add([]byte{})
	f.Add([]byte{1, 6})
	f.Fuzz(func(t *testing.T, payload []byte) {
		checkMatchDecode(t, payload)
	})
}

func TestDecodeReplayMatchConfigRefusesUnsupportedVersions(t *testing.T) {
	payload := encodeMatchForTest(t, resolveMatch(t, validMatchRequest(t)))
	for _, version := range []uint16{0, 3, 65535} {
		c, err := DecodeReplayMatchConfig(payload, version)
		if err == nil || !strings.Contains(err.Error(), "configuration.version") || c.Digest() != ([32]byte{}) {
			t.Errorf("version %d: expected explicit refusal, got %v", version, err)
		}
	}
}

package session

import (
	"bytes"
	"crypto/sha256"
	"reflect"
	"strings"
	"testing"
)

// extensionRecordForTest writes the tail's primitive values independently
// of the production writer. The caller adds the terminating zero ID.
func extensionRecordForTest(id uint64, flags byte, data []byte) []byte {
	out := append(uvarintBytes(id), flags)
	out = append(out, uvarintBytes(uint64(len(data)))...)
	return append(out, data...)
}

func withMatchTailForTest(base, tail []byte) []byte {
	return append(append([]byte(nil), base...), tail...)
}

func TestMatchConfigOptionalExtensionsRoundTripAndOwnStorage(t *testing.T) {
	r := validMatchRequest(t)
	// The tail starts after all of field 16, including its records.
	r.RoomContent = []MatchContentEntry{{Kind: MatchContentBaseArchive, Name: "totala1.hpi", Size: 42, Digest: sha256.Sum256([]byte("archive"))}}
	base := encodeMatchForTest(t, resolveMatch(t, r))
	r.Extensions = []MatchConfigExtension{{ID: 32, Data: []byte{0xff, 0, 0x80}}, {ID: 65535, Data: []byte{}}}
	c := resolveMatch(t, r)
	want := withMatchTailForTest(base, extensionRecordForTest(32, 0, []byte{0xff, 0, 0x80}))
	want = append(want, extensionRecordForTest(65535, 0, nil)...)
	want = append(want, 0)
	if got := encodeMatchForTest(t, c); !bytes.Equal(got, want) {
		t.Fatalf("tail encoding: got %x, want %x", got[len(base):], want[len(base):])
	}
	if c.Request().Extensions[1].Data != nil {
		t.Fatal("an empty extension payload was not normalized to nil")
	}
	for _, decode := range []func([]byte) (EffectiveMatchConfig, error){DecodeMatchConfig, func(b []byte) (EffectiveMatchConfig, error) { return DecodeReplayMatchConfig(b, 2) }} {
		input := append([]byte(nil), want...)
		back, err := decode(input)
		if err != nil {
			t.Fatal(err)
		}
		if back.Digest() != c.Digest() || !reflect.DeepEqual(back.Request(), c.Request()) || !bytes.Equal(encodeMatchForTest(t, back), want) {
			t.Fatal("opaque extension bytes or their identity did not round-trip")
		}
		input[len(base)+3] ^= 0xff
		if !reflect.DeepEqual(back.Request(), c.Request()) || !bytes.Equal(encodeMatchForTest(t, back), want) {
			t.Fatal("decoded extension payload aliases the input")
		}
	}
	r.Extensions[0].Data[0] = 1
	r.Extensions[1].ID = 100
	copyRequest := c.Request()
	copyRequest.Extensions[0].Data[1] = 1
	copyRequest.Extensions[1].ID = 200
	if !bytes.Equal(encodeMatchForTest(t, c), want) || !reflect.DeepEqual(c.Request().Extensions[0].Data, []byte{0xff, 0, 0x80}) || c.Request().Extensions[1].ID != 65535 {
		t.Fatal("request storage reached a frozen extension")
	}
	wantDigest := sha256.Sum256(append([]byte("nanolathe/match-config/2"), want...))
	if c.Digest() != wantDigest {
		t.Fatal("extension bytes were not hashed in the existing version-2 domain")
	}
	for _, change := range []func(*MatchConfigRequest){
		func(q *MatchConfigRequest) { q.Extensions[0].Data[0] ^= 1 },
		func(q *MatchConfigRequest) { q.Extensions[0].ID++ },
		func(q *MatchConfigRequest) { q.Extensions = q.Extensions[:1] },
	} {
		q := c.Request()
		change(&q)
		if resolveMatch(t, q).Digest() == c.Digest() {
			t.Fatal("changing opaque extension bytes did not change identity")
		}
	}
	r.Extensions = []MatchConfigExtension{}
	if empty := resolveMatch(t, r); empty.Request().Extensions != nil || !bytes.Equal(encodeMatchForTest(t, empty), base) {
		t.Fatal("an empty extension collection changed the current encoding")
	}
}

func TestMatchConfigExtensionAdmissionBoundaries(t *testing.T) {
	r := validMatchRequest(t)
	r.Extensions = make([]MatchConfigExtension, 128)
	for i := range r.Extensions {
		r.Extensions[i].ID = uint16(32 + i)
	}
	r.Extensions[0].Data = make([]byte, 64<<10)
	c := resolveMatch(t, r)
	if !checkMatchDecode(t, encodeMatchForTest(t, c)) {
		t.Fatal("128 records and a 64 KiB payload were refused")
	}
	for _, tc := range []struct {
		name   string
		change func(*MatchConfigRequest)
		want   string
	}{
		{"129 records", func(q *MatchConfigRequest) { q.Extensions = append(q.Extensions, MatchConfigExtension{ID: 200}) }, "at most 128"},
		{"64 KiB plus one", func(q *MatchConfigRequest) { q.Extensions[0].Data = make([]byte, 64<<10+1) }, "at most 65536"},
		{"reserved", func(q *MatchConfigRequest) { q.Extensions[0].ID = 16 }, "reserved"},
		{"terminator ID", func(q *MatchConfigRequest) { q.Extensions[0].ID = 0 }, "reserved"},
		{"duplicate", func(q *MatchConfigRequest) { q.Extensions[1].ID = 32 }, "strictly ascending"},
		{"descending", func(q *MatchConfigRequest) { q.Extensions[0].ID = 1000 }, "strictly ascending"},
		{"required unknown", func(q *MatchConfigRequest) { q.Extensions[0].Required = true }, "required match configuration extension 32"},
		{"total size", func(q *MatchConfigRequest) {
			for i := 0; i < 32; i++ {
				q.Extensions[i].Data = make([]byte, 64<<10)
			}
		}, "encoding of at most"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := c.Request()
			tc.change(&q)
			bad, err := ResolveMatchConfig(q)
			if err == nil || !strings.Contains(err.Error(), tc.want) || bad.Digest() != ([32]byte{}) {
				t.Fatalf("expected %q refusal, got %v", tc.want, err)
			}
		})
	}
}

func TestDecodeMatchConfigRefusesMalformedExtensionTails(t *testing.T) {
	base := encodeMatchForTest(t, resolveMatch(t, validMatchRequest(t)))
	valid := extensionRecordForTest(32, 0, []byte{7})
	var tooMany []byte
	for i := uint64(32); i < 32+129; i++ {
		tooMany = append(tooMany, extensionRecordForTest(i, 0, nil)...)
	}
	tooMany = append(tooMany, 0)
	for _, tc := range []struct {
		name string
		tail []byte
		want string
	}{
		{"empty", []byte{0}, "no extension tail"},
		{"reserved", []byte{16, 0, 0, 0}, "reserved"},
		{"duplicate", append(append(append([]byte(nil), valid...), valid...), 0), "strictly ascending"},
		{"descending", append(append(append([]byte(nil), valid...), extensionRecordForTest(31, 0, nil)...), 0), "strictly ascending"},
		{"unknown required", []byte{32, 1, 0, 0}, "required match configuration extension 32"},
		{"other flags", []byte{32, 2, 0, 0}, "only bit 0"},
		{"required and other flags", []byte{32, 3, 0, 0}, "only bit 0"},
		{"ID overflow", []byte{0x80, 0x80, 4}, "within 16 bits"},
		{"overlong ID", []byte{0xa0, 0}, "shortest varint"},
		{"truncated ID", []byte{0x80}, "complete varint"},
		{"truncated flags", []byte{32}, "another byte"},
		{"length overflow", []byte{32, 0, 0x80, 0x80, 0x80, 0x80, 0x10}, "within 32 bits"},
		{"overlong length", []byte{32, 0, 0x80, 0}, "shortest varint"},
		{"truncated length", []byte{32, 0, 0x80}, "complete varint"},
		{"excessive length", append([]byte{32, 0}, uvarintBytes(64<<10+1)...), "at most 65536"},
		{"truncated payload", []byte{32, 0, 2, 7}, "2 more bytes"},
		{"missing terminator", valid, "complete varint"},
		{"trailing bytes", append(append([]byte(nil), valid...), 0, 0), "no bytes after"},
		{"too many", tooMany, "at most 128"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad, err := DecodeMatchConfig(withMatchTailForTest(base, tc.tail))
			if err == nil || !strings.Contains(err.Error(), tc.want) || bad.Digest() != ([32]byte{}) {
				t.Fatalf("expected %q refusal, got %v", tc.want, err)
			}
		})
	}
}

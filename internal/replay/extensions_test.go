package replay

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/netproto"
)

// legacyHeaderFixture writes the layouts predating extensions independently
// of encodeHeader, so compatibility does not follow the current encoder.
func legacyHeaderFixture(version uint16) (Header, []byte) {
	h := testHeader(KindSkirmish)
	h.Identity.Protocol = 1
	if version == 1 {
		h.Addons = nil
	}
	var w netproto.Writer
	w.U8(uint8(h.Kind))
	w.U32(3)
	w.Raw([]byte{1, 2, 3})
	w.U16(1)
	w.Digest([32]byte{})
	w.Digest([32]byte{4})
	w.Digest([32]byte{})
	w.Key("modern")
	w.U8(2)
	w.Digest([32]byte{6})
	w.Text("mod")
	w.Text("1.0")
	w.Digest([32]byte{7})
	w.Digest([32]byte{5})
	w.Digest([32]byte{9})
	w.U8(0)
	w.Text("portable")
	w.U32(2)
	w.Text("Player")
	w.Text("ARM")
	w.U8(0)
	w.U8(1)
	w.Text("Computer")
	w.Text("CORE")
	w.U8(1)
	w.U8(2)
	w.S64(1760000000000)
	if version == 2 {
		w.U32(1)
		w.Text("cavedog-bonus-units")
		w.Text("original")
		w.U64(236)
		w.Digest([32]byte{8})
	}
	return h, w.Bytes()
}

func fileWithHeader(version uint16, header, stream []byte) []byte {
	var w netproto.Writer
	w.Raw([]byte(Magic))
	w.U16(version)
	w.U32(uint32(len(header)))
	w.Raw(header)
	w.Raw(stream)
	return w.Bytes()
}

// Both legacy layouts retain their exact boundary before the same stream.
// The header encoder always uses the current header layout.
func TestLegacyHeaderVersions(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		h, raw := legacyHeaderFixture(version)
		data, _ := record(t, h, []op{{kind: EntryPumps, tick: 1, ticks: 1}}, EndFinished)
		_, streamAt, err := readPreamble(data)
		if err != nil {
			t.Fatal(err)
		}
		data = fileWithHeader(version, raw, data[streamAt:])
		r, err := NewReader(data)
		if err != nil {
			t.Fatalf("version %d: %v", version, err)
		}
		got := r.Header()
		if got.FileVersion() != version {
			t.Fatalf("version %d reports %d", version, got.FileVersion())
		}
		// Re-encoding ignores only the source-file version, preserving every
		// field, and writes the current version's add-ons and extension area.
		encoded, err := encodeHeader(got)
		if err != nil {
			t.Fatal(err)
		}
		current, err := decodeHeader(encoded)
		if err != nil || current.FileVersion() != 2 || !reflect.DeepEqual(current, h) {
			t.Fatalf("version %d rewritten header = %+v, %v", version, current, err)
		}
		entries, err := readAll(t, data)
		if err != io.EOF || len(entries) != 2 || entries[0].Kind != EntryPumps || entries[0].Tick != 1 || entries[1].Reason != EndFinished {
			t.Fatalf("version %d stream = %+v, %v", version, entries, err)
		}
		for cut := range len(raw) {
			if _, err := NewReader(fileWithHeader(version, raw[:cut], nil)); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("version %d truncated header at %d: %v", version, cut, err)
			}
		}
	}
	_, v1 := legacyHeaderFixture(1)
	if _, err := NewReader(fileWithHeader(1, append(v1, 0), nil)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("version 1 accepted bytes after Started: %v", err)
	}
	if _, err := NewReader(fileWithHeader(2, v1, nil)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("version 2 accepted a missing add-on count: %v", err)
	}
	if (Header{}).FileVersion() != 2 {
		t.Fatal("a new Header does not use version 2")
	}
}

// Optional metadata changes only the sized header. Chunks retain their exact
// bytes and decode to the same commands, pumps, checksums and end entry.
func TestHeaderExtensionsPreserveStreamAndCopies(t *testing.T) {
	h := testHeader(KindSkirmish)
	ops := []op{
		{kind: EntryCommand, tick: 1, position: 1, payload: []byte{7}},
		{kind: EntryPumps, tick: 1, ticks: 1},
		{kind: EntryChecksum, tick: 1, sum: [32]byte{8}},
	}
	plain, _ := record(t, h, ops, EndFinished)
	h.Extensions = []HeaderExtension{{ID: 1, Data: []byte{0, 1, 0xff}}, {ID: 65535, Data: []byte{2}}}
	data, _ := record(t, h, ops, EndFinished)
	_, plainAt, _ := readPreamble(plain)
	_, extendedAt, err := readPreamble(data)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plain[plainAt:], data[extendedAt:]) {
		t.Fatal("header metadata changed the chunk stream")
	}
	if plainAt != extendedAt {
		t.Fatal("small metadata did not fit inside the existing header reserve")
	}
	entries, err := readAll(t, data)
	if err != io.EOF || !reflect.DeepEqual(replayed(entries), ops) {
		t.Fatalf("extended header changed stream entries: %+v, %v", entries, err)
	}
	r, err := NewReader(data)
	if err != nil {
		t.Fatal(err)
	}
	got := r.Header()
	if !reflect.DeepEqual(got, h) {
		t.Fatalf("header = %+v, want %+v", got, h)
	}
	again, _ := record(t, got, ops, EndFinished)
	if !bytes.Equal(again, data) {
		t.Fatal("unknown optional extensions did not round-trip verbatim")
	}
	got.Extensions[0].Data[0] = 99
	got.Extensions[1].ID = 7
	if !reflect.DeepEqual(r.Header(), h) {
		t.Fatal("Header() aliases the reader's extensions")
	}
	data[plainAt-headerExtensionReserve+3] ^= 1 // first field's payload
	if !reflect.DeepEqual(r.Header(), h) {
		t.Fatal("the reader's extension payload aliases the input")
	}
}

func extensionRecord(id uint16, flags uint8, size uint32, payload []byte) []byte {
	var w netproto.Writer
	w.U16(id)
	w.U8(flags)
	w.U32(size)
	w.Raw(payload)
	return w.Bytes()
}

func TestHeaderExtensionReserveAndBounds(t *testing.T) {
	h, old := legacyHeaderFixture(2)
	raw, err := encodeHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, append(append([]byte(nil), old...), make([]byte, 256)...)) {
		t.Fatal("new headers do not append exactly 256 reserved zero bytes")
	}
	for _, payload := range []int{251, 252} {
		h.Extensions = []HeaderExtension{{ID: 1, Data: make([]byte, payload)}}
		raw, err = encodeHeader(h)
		wantArea := max(256, payload+5) // ID, flags, two-byte length, terminator
		if err != nil || len(raw)-len(old) != wantArea {
			t.Fatalf("%d-byte metadata payload: area %d, want %d: %v", payload, len(raw)-len(old), wantArea, err)
		}
		if got, err := decodeHeader(raw); err != nil || !reflect.DeepEqual(got, h) {
			t.Fatalf("reserve boundary read: %v", err)
		}
	}
	h.Extensions = []HeaderExtension{{ID: 1, Data: bytes.Repeat([]byte{7}, maxHeaderExtensionBytes)}}
	raw, err = encodeHeader(h)
	if err != nil {
		t.Fatalf("maximum payload: %v", err)
	}
	if got, err := decodeHeader(raw); err != nil || !reflect.DeepEqual(got, h) {
		t.Fatalf("maximum payload read: %v", err)
	}
	h.Extensions = make([]HeaderExtension, maxHeaderExtensions)
	for i := range h.Extensions {
		h.Extensions[i].ID = uint16(i + 1)
	}
	raw, err = encodeHeader(h)
	if err != nil {
		t.Fatalf("maximum count: %v", err)
	}
	if got, err := decodeHeader(raw); err != nil || !reflect.DeepEqual(got, h) {
		t.Fatalf("maximum count read: %v", err)
	}
	for _, tail := range [][]byte{{0}, {0, 0, 0}} {
		if _, err := decodeHeader(append(append([]byte(nil), old...), tail...)); err != nil {
			t.Fatalf("terminated zero padding %v: %v", tail, err)
		}
	}
	// Individually valid fields still share the existing total-header bound.
	h.Extensions = make([]HeaderExtension, MaxHeaderBytes/maxHeaderExtensionBytes)
	payload := make([]byte, maxHeaderExtensionBytes)
	for i := range h.Extensions {
		h.Extensions[i] = HeaderExtension{ID: uint16(i + 1), Data: payload}
	}
	if _, err := NewWriter(io.Discard, h); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("total header bound: %v", err)
	}
	if _, err := decodeHeader(make([]byte, MaxHeaderBytes+1)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("oversize decoded header: %v", err)
	}
}

func TestHeaderExtensionRefusals(t *testing.T) {
	h, old := legacyHeaderFixture(2)
	tooMany := make([]HeaderExtension, maxHeaderExtensions+1)
	var tooManyRaw []byte
	for i := range tooMany {
		tooMany[i].ID = uint16(i + 1)
		tooManyRaw = append(tooManyRaw, extensionRecord(uint16(i+1), 0, 0, nil)...)
	}
	for name, fields := range map[string][]HeaderExtension{
		"zero ID":     {{ID: 0}},
		"duplicate":   {{ID: 1}, {ID: 1}},
		"unsorted":    {{ID: 2}, {ID: 1}},
		"count":       tooMany,
		"payload cap": {{ID: 1, Data: make([]byte, maxHeaderExtensionBytes+1)}},
	} {
		h.Extensions = fields
		var out bytes.Buffer
		if _, err := NewWriter(&out, h); !errors.Is(err, ErrCorrupt) || out.Len() != 0 {
			t.Errorf("writer %s: %v, %d bytes written", name, err, out.Len())
		}
	}
	for name, tail := range map[string][]byte{
		"flags":              {1, 2, 0, 0},
		"required flags":     {1, 3, 0, 0},
		"truncated ID":       {0x80},
		"ID overflow":        {0x80, 0x80, 4},
		"overlong ID":        {0x81, 0, 0, 0, 0},
		"truncated flags":    {1},
		"truncated size":     {1, 0, 0x80},
		"size overflow":      {1, 0, 0x80, 0x80, 0x80, 0x80, 0x10},
		"overlong size":      {1, 0, 0x80, 0, 0},
		"truncated payload":  {1, 0, 2, 7},
		"payload cap":        extensionRecord(1, 0, maxHeaderExtensionBytes+1, nil),
		"missing terminator": {1, 0, 0},
		"nonzero padding":    {0, 0, 1},
		"record after end":   {0, 1, 0, 0},
		"duplicate":          {1, 0, 0, 1, 0, 0, 0},
		"unsorted":           {2, 0, 0, 1, 0, 0, 0},
		"count":              append(tooManyRaw, 0),
	} {
		if _, err := NewReader(fileWithHeader(2, append(append([]byte(nil), old...), tail...), nil)); !errors.Is(err, ErrCorrupt) {
			t.Errorf("reader %s: %v", name, err)
		}
	}
	h.Extensions = []HeaderExtension{{ID: 1, Required: true, Data: []byte{7}}}
	var out bytes.Buffer
	if _, err := NewWriter(&out, h); !errors.Is(err, ErrUnsupportedFeature) || out.Len() != 0 {
		t.Fatalf("required unknown writer: %v, %d bytes written", err, out.Len())
	}
	tail := append(extensionRecord(1, 1, 1, []byte{7}), 0)
	if _, err := NewReader(fileWithHeader(2, append(old, tail...), nil)); !errors.Is(err, ErrUnsupportedFeature) {
		t.Fatalf("required unknown reader: %v", err)
	}
}

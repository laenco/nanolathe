package replay

import (
	"bytes"
	"reflect"
	"testing"
)

// FuzzHeaderExtensions concentrates mutations after the frozen version-2
// prefix and checks that accepted optional metadata survives rewriting.
func FuzzHeaderExtensions(f *testing.F) {
	_, prefix := legacyHeaderFixture(2)
	for _, tail := range [][]byte{
		nil,
		{0},
		{0, 0, 1},
		{1, 0, 1, 7, 0},
		{1, 1, 0, 0},
		{0x80},
		{2, 0, 0, 1, 0, 0, 0},
	} {
		f.Add(tail)
	}
	f.Fuzz(func(t *testing.T, tail []byte) {
		if len(tail) > MaxHeaderBytes-len(prefix) {
			return
		}
		payload := append(append([]byte(nil), prefix...), tail...)
		h, err := decodeHeader(payload)
		if err != nil {
			return
		}
		encoded, err := encodeHeader(h)
		if err != nil {
			// An accepted header may leave too little room for the writer's
			// reserved padding at the total header limit.
			if len(payload)+headerExtensionReserve > MaxHeaderBytes {
				return
			}
			t.Fatal(err)
		}
		got, err := decodeHeader(encoded)
		if err != nil || !reflect.DeepEqual(got, h) {
			t.Fatalf("accepted extensions changed on rewrite: %v", err)
		}
	})
}

// FuzzReader feeds arbitrary bytes to the Reader and the summary: neither
// may panic or allocate beyond the bounds it checks, whatever the input.
func FuzzReader(f *testing.F) {
	var out bytes.Buffer
	w, err := NewWriter(&out, testHeader(KindSkirmish))
	if err != nil {
		f.Fatal(err)
	}
	for _, o := range singlePlayerScript(60) {
		switch {
		case o.flush:
			err = w.Flush()
		case o.kind == EntryCommand:
			err = w.Command(o.tick, o.seat, o.position, o.payload)
		case o.kind == EntryChecksum:
			err = w.Checksum(o.tick, o.sum)
		case o.kind == EntryPumps:
			err = w.Pump(o.tick, o.ticks)
		}
		if err != nil {
			f.Fatal(err)
		}
	}
	if err := w.Close(EndFinished); err != nil {
		f.Fatal(err)
	}
	valid := out.Bytes()
	f.Add(valid)
	f.Add(valid[:len(valid)/2])
	flipped := append([]byte(nil), valid...)
	flipped[len(flipped)-5] ^= 0x40
	f.Add(flipped)
	f.Add([]byte(Magic))
	for _, version := range []uint16{1, 2} {
		_, header := legacyHeaderFixture(version)
		f.Add(fileWithHeader(version, header, nil))
	}
	h := testHeader(KindSkirmish)
	h.Extensions = []HeaderExtension{{ID: 65535, Data: []byte{0, 0xff}}}
	header, err := encodeHeader(h)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(fileWithHeader(2, header, nil))
	f.Fuzz(func(t *testing.T, data []byte) {
		if r, err := NewReader(data); err == nil {
			_ = r.Header()
			for range 1 << 16 {
				if _, err := r.Next(); err != nil {
					break
				}
			}
		}
		_, _ = Summarize(data)
	})
}

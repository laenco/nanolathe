package replay

import (
	"fmt"

	"github.com/nanolathe-gg/nanolathe/internal/netproto"
)

// HeaderExtension is one version-2 header field. Unknown optional fields are
// retained verbatim; unknown required fields prevent playback or writing
// (DESIGN_MULTIPLAYER §10). No extension IDs are assigned in this build.
type HeaderExtension struct {
	ID       uint16
	Required bool
	Data     []byte
}

const (
	maxHeaderExtensions     = 128
	maxHeaderExtensionBytes = 64 << 10
	// Metadata consumes this minimum area; the unused tail is zero padding,
	// including at least one byte for the ID terminator.
	headerExtensionReserve = 256
)

func unsupportedHeaderExtension(id uint16) error {
	return fmt.Errorf("%w: logical path header.extensions[%d], providers searched [replay format v%d], expected a supported required extension", ErrUnsupportedFeature, id, FormatVersion)
}

func validateHeaderExtensions(fields []HeaderExtension) error {
	if len(fields) > maxHeaderExtensions {
		return formatError("header.extensions", fmt.Sprintf("at most %d extensions", maxHeaderExtensions))
	}
	for i, field := range fields {
		path := fmt.Sprintf("header.extensions[%d]", i)
		if field.ID == 0 || i > 0 && fields[i-1].ID >= field.ID {
			return formatError(path, "nonzero extension IDs in strictly ascending order")
		}
		if len(field.Data) > maxHeaderExtensionBytes {
			return formatError(path, fmt.Sprintf("at most %d payload bytes", maxHeaderExtensionBytes))
		}
		if field.Required {
			return unsupportedHeaderExtension(field.ID)
		}
	}
	return nil
}

// writeHeaderExtensions fills the 256-byte reserve before growing the area.
// The enclosing header size frames it; the unused tail stays zero.
func writeHeaderExtensions(w *netproto.Writer, fields []HeaderExtension) error {
	used := 0
	for _, field := range fields {
		used += netproto.UvarintLen(uint64(field.ID)) + 1 + netproto.UvarintLen(uint64(len(field.Data))) + len(field.Data)
	}
	padding := max(1, headerExtensionReserve-used)
	size := w.Len() + used + padding
	if size > MaxHeaderBytes {
		return formatError("header", fmt.Sprintf("at most %d bytes", MaxHeaderBytes))
	}
	for _, field := range fields {
		w.U16(field.ID)
		var flags uint8
		if field.Required {
			flags = 1
		}
		w.U8(flags)
		w.U32(uint32(len(field.Data)))
		w.Raw(field.Data)
	}
	w.Raw(make([]byte, padding))
	return nil
}

// readHeaderExtensions accepts any amount of zero padding after the
// terminator. A present area must contain that terminator, even when empty.
func readHeaderExtensions(r *netproto.Reader, end int) []HeaderExtension {
	var fields []HeaderExtension
	for {
		id := r.U16()
		if r.Err() != nil {
			return nil
		}
		if id == 0 {
			for _, b := range r.Raw(end - r.Offset()) {
				if b != 0 {
					r.Fail("only zero padding after the extension terminator")
					return nil
				}
			}
			return fields
		}
		if len(fields) == maxHeaderExtensions {
			r.Fail(fmt.Sprintf("at most %d extensions", maxHeaderExtensions))
			return nil
		}
		if len(fields) > 0 && fields[len(fields)-1].ID >= id {
			r.Fail("nonzero extension IDs in strictly ascending order")
			return nil
		}
		flags := r.U8()
		if r.Err() == nil && flags&^uint8(1) != 0 {
			r.Fail("extension flags with only bit 0 (required) set")
		}
		size := r.U32()
		if r.Err() == nil && size > maxHeaderExtensionBytes {
			r.Fail(fmt.Sprintf("at most %d extension payload bytes", maxHeaderExtensionBytes))
		}
		data := r.Raw(int(size))
		if r.Err() != nil {
			return nil
		}
		if flags&1 != 0 {
			r.Abort(unsupportedHeaderExtension(id))
			return nil
		}
		fields = append(fields, HeaderExtension{ID: id, Data: append([]byte(nil), data...)})
	}
}

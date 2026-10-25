package formats

import (
	"bytes"
	"fmt"
	"strings"
)

// TDFWriteSection is one TDF section to encode (the map generator,
// docs/DESIGN_MAP_GENERATOR.md): its assignments in order, then its nested
// sections, laid out as retail's OTA files are [fmt tdf] — a bracketed name,
// a braced body, tab indentation and CRLF line ends.
type TDFWriteSection struct {
	Name     string
	Keys     []TDFWriteKey
	Sections []*TDFWriteSection
}

// TDFWriteKey is one `key=value;` assignment.
type TDFWriteKey struct{ Key, Value string }

// Set appends an assignment and returns the section, for chaining.
func (s *TDFWriteSection) Set(key, value string) *TDFWriteSection {
	s.Keys = append(s.Keys, TDFWriteKey{Key: key, Value: value})
	return s
}

// Add appends a nested section and returns it.
func (s *TDFWriteSection) Add(name string) *TDFWriteSection {
	child := &TDFWriteSection{Name: name}
	s.Sections = append(s.Sections, child)
	return child
}

// EncodeTDF writes sections as TDF text that ParseTDF reads back. A name,
// key or value that the syntax cannot carry — a bracket, brace, semicolon,
// equals sign in a key, a line break, or `//` or `/*`, which open a comment
// anywhere on a line [fmt tdf] — is refused rather than escaped, since TDF
// has no escapes.
func EncodeTDF(sections ...*TDFWriteSection) ([]byte, error) {
	var b bytes.Buffer
	for _, s := range sections {
		if err := encodeTDFSection(&b, s, 0); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}

func encodeTDFSection(b *bytes.Buffer, s *TDFWriteSection, depth int) error {
	if err := tdfWritable("section name", s.Name, "[]{};\r\n"); err != nil {
		return err
	}
	indent := strings.Repeat("\t", depth)
	fmt.Fprintf(b, "%s[%s]\r\n%s\t{\r\n", indent, s.Name, indent)
	for _, k := range s.Keys {
		if err := tdfWritable("key", k.Key, "[]{};=\r\n"); err != nil {
			return err
		}
		if err := tdfWritable("value", k.Value, "[]{};\r\n"); err != nil {
			return err
		}
		fmt.Fprintf(b, "%s\t%s=%s;\r\n", indent, k.Key, k.Value)
	}
	for _, child := range s.Sections {
		if err := encodeTDFSection(b, child, depth+1); err != nil {
			return err
		}
	}
	fmt.Fprintf(b, "%s\t}\r\n", indent)
	return nil
}

func tdfWritable(what, text, forbidden string) error {
	if what != "value" && strings.TrimSpace(text) == "" {
		return fmt.Errorf("tdf write: empty %s", what)
	}
	if strings.ContainsAny(text, forbidden) || strings.Contains(text, "//") || strings.Contains(text, "/*") {
		return fmt.Errorf("tdf write: %s %q holds a character TDF cannot carry", what, text)
	}
	return nil
}

package cli

import (
	"io"
	"strings"
	"unicode"
)

// Printable removes control characters from text bound for a terminal, keeping
// newlines and tabs.
//
// What a login prints, and the errors it returns, carry strings a server
// chose: an issuer, scope names, URLs, an error description. A terminal acts
// on escape sequences in whatever it is given — move the cursor, rewrite a
// line already shown, set the window title — so those strings are made inert
// at the one place they leave the program, not at each place they are
// formatted.
func Printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// printableWriter applies Printable to everything written through it.
type printableWriter struct{ w io.Writer }

// printable wraps a writer that leads to a terminal.
func printable(w io.Writer) io.Writer {
	if w == nil {
		return nil
	}
	return printableWriter{w}
}

// Write reports the caller's own length on success: the bytes dropped were
// never meant to arrive, and a short count would read as a failed write.
func (p printableWriter) Write(b []byte) (int, error) {
	if _, err := io.WriteString(p.w, Printable(string(b))); err != nil {
		return 0, err
	}
	return len(b), nil
}

package cli

import (
	"io"
	"strings"
	"unicode"
)

// Printable removes from text bound for a terminal the characters a terminal
// acts on rather than shows: the control characters (C0, DEL and C1) other
// than newline and tab, and the bidirectional controls. Invalid UTF-8 comes
// out as U+FFFD.
//
// What a login prints, and the errors it returns, carry strings a server
// chose: an issuer, scope names, URLs, an error description. A terminal obeys
// escape sequences in whatever it is given — move the cursor, rewrite a line
// already shown, set the window title, write the clipboard — and a
// bidirectional override makes text read in an order it was not written in,
// which is enough to show one URL while another is used. So those strings are
// made inert at the one place they leave the program, not at each place they
// are formatted.
//
// Only the characters go, not what follows them: without ESC or a C1
// introducer the rest of a sequence is plain text, and removing "the whole
// sequence" would mean guessing where an unterminated one ends.
//
// Newlines stay, because the program's own messages have them. A server can
// therefore still break a line of its own text in two; it cannot move the
// cursor back over what the program printed (ADR-0006).
func Printable(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case unicode.IsControl(r), unicode.Is(unicode.Bidi_Control, r):
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
//
// Each call is filtered on its own. That is sound for how it is used — every
// write is one whole formatted message — and would not be for a stream that
// splits a character across two writes.
func (p printableWriter) Write(b []byte) (int, error) {
	if _, err := io.WriteString(p.w, Printable(string(b))); err != nil {
		return 0, err
	}
	return len(b), nil
}

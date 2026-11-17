package oauth

import "strings"

// challenge is one entry of a WWW-Authenticate header: a scheme and its
// parameters, with the parameter names lowercased.
type challenge struct {
	scheme string
	params map[string]string
}

// bearerParam returns a parameter of the server's Bearer challenge, or "" when
// there is none.
//
// The header values are taken in order and the first Bearer challenge that
// carries a non-empty value for the name wins. A value that does not fit the
// header's grammar is skipped whole rather than read approximately: this is
// where a hostile or broken server chooses what the login is told.
func bearerParam(headerValues []string, name string) string {
	name = strings.ToLower(name)
	for _, value := range headerValues {
		challenges, ok := parseChallenges(value)
		if !ok {
			continue
		}
		for _, c := range challenges {
			if !strings.EqualFold(c.scheme, "Bearer") {
				continue
			}
			if v := c.params[name]; v != "" {
				return v
			}
		}
	}
	return ""
}

// parseChallenges reads one WWW-Authenticate header value by its grammar
// (RFC 9110 §11.6.1):
//
//	WWW-Authenticate = #challenge
//	challenge        = auth-scheme [ 1*SP ( token68 / #auth-param ) ]
//	auth-param       = token BWS "=" BWS ( token / quoted-string )
//
// The list of challenges and each challenge's list of parameters share the
// comma as their separator, so the value is read as comma-separated items and
// each item is either a parameter of the challenge before it or the start of a
// new challenge. It reports false when any item is neither.
//
// Searching the string for `scope="` instead would find those characters
// inside another parameter's quoted text — an error_description, say — and
// inside a parameter whose name merely ends in "scope".
func parseChallenges(value string) ([]challenge, bool) {
	var out []challenge
	s := scanner{src: value}
	for {
		s.skipSeparators()
		if s.done() {
			return out, true
		}

		first := s.token()
		if first == "" {
			return nil, false
		}
		spaces := s.skipSpaces()

		// token BWS "=" value is a parameter of the challenge before it. A
		// parameter with no challenge to belong to is not a header this code
		// should guess at.
		if s.peek() == '=' {
			if len(out) == 0 {
				return nil, false
			}
			value, ok := s.paramValue()
			if !ok {
				return nil, false
			}
			out[len(out)-1].params[strings.ToLower(first)] = value
			if !s.atItemEnd() {
				return nil, false
			}
			continue
		}

		// Otherwise the token is a scheme, alone or followed by its first
		// parameter or by a token68.
		c := challenge{scheme: first, params: map[string]string{}}
		if s.atItemEnd() {
			out = append(out, c)
			continue
		}
		if spaces == 0 {
			return nil, false
		}

		mark := s.pos
		if name := s.token(); name != "" {
			s.skipSpaces()
			if s.peek() == '=' {
				if value, ok := s.paramValue(); ok && s.atItemEnd() {
					c.params[strings.ToLower(name)] = value
					out = append(out, c)
					continue
				}
			}
		}
		s.pos = mark
		if !s.token68() || !s.atItemEnd() {
			return nil, false
		}
		out = append(out, c)
	}
}

// scanner walks a header value.
type scanner struct {
	src string
	pos int
}

func (s *scanner) done() bool { return s.pos >= len(s.src) }

func (s *scanner) peek() byte {
	if s.done() {
		return 0
	}
	return s.src[s.pos]
}

// skipSpaces consumes optional whitespace and reports how much there was.
func (s *scanner) skipSpaces() int {
	start := s.pos
	for !s.done() && (s.src[s.pos] == ' ' || s.src[s.pos] == '\t') {
		s.pos++
	}
	return s.pos - start
}

// skipSeparators consumes the commas and whitespace between items. Empty list
// elements are allowed by the list grammar (RFC 9110 §5.6.1).
func (s *scanner) skipSeparators() {
	for !s.done() && (s.src[s.pos] == ' ' || s.src[s.pos] == '\t' || s.src[s.pos] == ',') {
		s.pos++
	}
}

// atItemEnd reports whether the scanner stands at a comma or the end, allowing
// whitespace before it.
func (s *scanner) atItemEnd() bool {
	s.skipSpaces()
	return s.done() || s.src[s.pos] == ','
}

// token consumes an RFC 9110 token and returns it, or "" when there is none.
func (s *scanner) token() string {
	start := s.pos
	for !s.done() && isTokenChar(s.src[s.pos]) {
		s.pos++
	}
	return s.src[start:s.pos]
}

// token68 consumes credentials in the token68 form and reports whether there
// were any.
func (s *scanner) token68() bool {
	start := s.pos
	for !s.done() && isToken68Char(s.src[s.pos]) {
		s.pos++
	}
	if s.pos == start {
		return false
	}
	for !s.done() && s.src[s.pos] == '=' {
		s.pos++
	}
	return true
}

// paramValue consumes `= value` with the scanner standing on the "=", and
// returns the value with a quoted-string unquoted.
func (s *scanner) paramValue() (string, bool) {
	start := s.pos
	s.pos++ // the "="
	s.skipSpaces()
	if s.peek() == '"' {
		if v, ok := s.quotedString(); ok {
			return v, true
		}
		s.pos = start
		return "", false
	}
	if v := s.token(); v != "" {
		return v, true
	}
	s.pos = start
	return "", false
}

// quotedString consumes a quoted-string and returns its content with the
// quoted-pair escapes resolved (RFC 9110 §5.6.4).
func (s *scanner) quotedString() (string, bool) {
	s.pos++ // the opening quote
	var b strings.Builder
	for !s.done() {
		c := s.src[s.pos]
		switch {
		case c == '"':
			s.pos++
			return b.String(), true
		case c == '\\':
			s.pos++
			if s.done() {
				return "", false
			}
			b.WriteByte(s.src[s.pos])
		default:
			b.WriteByte(c)
		}
		s.pos++
	}
	return "", false // no closing quote
}

func isTokenChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

func isToken68Char(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("-._~+/", c) >= 0
}

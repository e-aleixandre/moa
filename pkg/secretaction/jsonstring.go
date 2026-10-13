package secretaction

import (
	"unicode/utf16"
	"unicode/utf8"
)

// DecodeJSONString decodes a JSON string literal into a fresh []byte the
// caller can wipe, without going through an immutable Go string (which
// encoding/json would produce and nobody could zero).
func DecodeJSONString(raw []byte) ([]byte, error) {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return nil, ErrInvalidValue
	}
	body := raw[1 : len(raw)-1]
	out := make([]byte, 0, len(body))
	fail := func() ([]byte, error) {
		Wipe(out)
		return nil, ErrInvalidValue
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c == '"' || c < 0x20 {
			return fail()
		}
		if c != '\\' {
			out = append(out, c)
			continue
		}
		i++
		if i >= len(body) {
			return fail()
		}
		switch body[i] {
		case '"', '\\', '/':
			out = append(out, body[i])
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			r, ok := hex4(body, i+1)
			if !ok {
				return fail()
			}
			i += 4
			if utf16.IsSurrogate(r) {
				r2, ok := rune(0), false
				if i+2 < len(body) && body[i+1] == '\\' && body[i+2] == 'u' {
					r2, ok = hex4(body, i+3)
				}
				if dec := utf16.DecodeRune(r, r2); ok && dec != utf8.RuneError {
					r = dec
					i += 6
				} else {
					r = utf8.RuneError
				}
			}
			out = utf8.AppendRune(out, r)
		default:
			return fail()
		}
	}
	if !utf8.Valid(out) {
		return fail()
	}
	return out, nil
}

func hex4(b []byte, at int) (rune, bool) {
	if at+4 > len(b) {
		return 0, false
	}
	var r rune
	for _, c := range b[at : at+4] {
		r <<= 4
		switch {
		case c >= '0' && c <= '9':
			r |= rune(c - '0')
		case c >= 'a' && c <= 'f':
			r |= rune(c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			r |= rune(c - 'A' + 10)
		default:
			return 0, false
		}
	}
	return r, true
}

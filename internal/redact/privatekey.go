package redact

import (
	"regexp"
	"strings"
)

// The private-key rule removes a PEM or PGP private key from its BEGIN line to its END
// line. Output that was cut short has no END line, so failing that it removes what
// follows BEGIN and still looks like a key: armor headers, then base64 lines, with
// line breaks either raw or JSON-escaped.
//
// It is written by hand rather than as one pattern because "up to END, or else the
// base64 block" makes a regexp look for END from every BEGIN, which is quadratic on
// input with many BEGIN lines.

const privateKeyMark = "[REDACTED:private-key]"

var (
	// "PRIVATE KEY BLOCK" is PGP.
	privateKeyBeginRe = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY(?: BLOCK)?-----`)
	privateKeyEndRe   = regexp.MustCompile(`-----END [A-Z ]*PRIVATE KEY(?: BLOCK)?-----`)
	// Headers such as "Proc-Type: 4,ENCRYPTED" or "Version: GnuPG v2", then the key.
	// A base64 line has to be long enough not to be an ordinary word, so prose after
	// a truncated key is kept.
	privateKeyBodyRe = regexp.MustCompile(`^(?:(?:\s|\\[nrt])+[A-Za-z][A-Za-z-]*: [^\r\n\\"]*)*(?:(?:\s|\\[nrt])*[A-Za-z0-9+/=]{20,})*`)
)

func redactPrivateKeys(s string) string {
	var b strings.Builder
	pos := 0
	// The next END line and the next quote are looked up once and reused until the
	// scan has passed them, so the work stays linear in the input.
	endFrom, endTo, quote := -1, -1, -1
	for {
		m := privateKeyBeginRe.FindStringIndex(s[pos:])
		if m == nil {
			break
		}
		begin, body := pos+m[0], pos+m[1]
		b.WriteString(s[pos:begin])
		b.WriteString(privateKeyMark)

		if endFrom < body && endFrom != len(s) {
			if e := privateKeyEndRe.FindStringIndex(s[body:]); e != nil {
				endFrom, endTo = body+e[0], body+e[1]
			} else {
				endFrom = len(s)
			}
		}
		if quote < body && quote != len(s) {
			if q := strings.IndexByte(s[body:], '"'); q >= 0 {
				quote = body + q
			} else {
				quote = len(s)
			}
		}
		// A key contains no quote, so an END line beyond one belongs to some other
		// string, and taking everything up to it would tear a JSON frame apart.
		if endFrom < quote {
			pos = endTo
		} else {
			pos = body + len(privateKeyBodyRe.FindString(s[body:]))
		}
	}
	if pos == 0 {
		return s
	}
	b.WriteString(s[pos:])
	return b.String()
}

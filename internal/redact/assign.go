package redact

import (
	"regexp"
	"strings"
)

// The secret-assignment rule keeps a key name and drops its value:
//
//	"password": "hunter2"   password=hunter2   Authorization: Basic dXNlcjpwYXNz
//
// A regexp finds the key; the value is read by hand, because where it ends depends on
// how it is quoted, and redaction runs over raw JSON frames that must stay valid JSON.
// So escapes are only ever removed whole, a quoted value runs to its closing quote
// rather than to the first space, and quotes may be escaped (\" or deeper), which is
// how JSON looks when it is carried inside a JSON string.

const secretMark = "[REDACTED:secret]"

// minSecretLen keeps short values such as null, true or "abc" readable.
const minSecretLen = 6

// secretKeyRe matches a key name, its optional closing quote, and the separator.
// Group 1 is the name, group 2 the quote. The name need not start at a word boundary
// (AWS_SECRET_ACCESS_KEY, x-api-key), but nothing may follow it, so usage counters
// such as input_tokens are not keys.
var secretKeyRe = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|refresh[_-]?token|token|client[_-]?secret|secret[_-]?(?:access[_-]?)?key|secret|password|passwd|private[_-]?key|credentials?|authorization)(\\*")?\s*[:=]\s*`)

// secretAssignmentRe is the rule's pattern as shown in rule listings: a key followed
// by the start of a value. The matching itself is done by redactAssignments.
var secretAssignmentRe = regexp.MustCompile(secretKeyRe.String() + `(?:\\*")?[^\s",}\]\[\\]`)

// bareValueRe is an unquoted value: it runs to whitespace, a quote or a JSON
// delimiter. Inside a JSON string a backslash starts an escape, which is taken whole
// (\\, \/, \uXXXX) or ends the value (\", \n, \t and the other whitespace escapes).
var bareValueRe = regexp.MustCompile(`^(?:[^\s",}\]\\]|\\[\\/u])+`)

// authSchemeRe is the scheme of an Authorization header, which is not the secret.
var authSchemeRe = regexp.MustCompile(`(?i)^(?:basic|bearer|digest|negotiate|ntlm|token|apikey|api-key|key|dpop|oauth|aws4-hmac-sha256|scram-sha-(?:1|256))[ \t]+`)

func redactAssignments(s string) string {
	var b strings.Builder
	pos := 0
	for pos < len(s) {
		m := secretKeyRe.FindStringSubmatchIndex(s[pos:])
		if m == nil {
			break
		}
		keyEnd := pos + m[1]
		name := s[pos+m[2] : pos+m[3]]
		keyQuote := ""
		if m[4] >= 0 {
			keyQuote = s[pos+m[4] : pos+m[5]]
		}
		b.WriteString(s[pos:keyEnd])
		pos = keyEnd
		isAuth := strings.EqualFold(name, "authorization")

		run := 0
		for keyEnd+run < len(s) && s[keyEnd+run] == '\\' {
			run++
		}
		if keyEnd+run < len(s) && s[keyEnd+run] == '"' {
			level := quoteLevel(run)
			start := keyEnd + run + 1
			// Without a key quoted at the same level ("password":"…"), this quote
			// may be the one that closes the string the key sits in. Then JSON
			// punctuation follows, and none of that is the value.
			sameLevel := keyQuote != "" && quoteLevel(len(keyQuote)-1) == level
			if !sameLevel && closesString(s[start:]) {
				continue
			}
			if red, end, ok := redactQuoted(s, start, level, isAuth); ok {
				b.WriteString(s[keyEnd:start])
				b.WriteString(red)
				pos = end
			}
			continue
		}

		red, n, ok := redactBare(s[keyEnd:], isAuth)
		if !ok {
			continue
		}
		// A value with no quotes after a quoted key is a JSON number or literal;
		// the marker has to be a string for the frame to stay JSON.
		if keyQuote != "" {
			// Quoted as deeply as the key; backslashes beyond that are the key's.
			q := strings.Repeat(`\`, quoteLevel(len(keyQuote)-1)) + `"`
			red = q + red + q
		}
		b.WriteString(red)
		pos = keyEnd + n
	}
	if pos == 0 {
		return s
	}
	b.WriteString(s[pos:])
	return b.String()
}

// redactQuoted returns the replacement for the inside of the quoted value that starts
// at start, and where that value ends. It returns false to leave the value alone: it
// is too short to be a secret, or an earlier rule or an earlier pass already redacted
// it. That is checked before looking for the end, so text that is skipped is not
// scanned once per key.
func redactQuoted(s string, start, level int, isAuth bool) (string, int, bool) {
	rest := s[start:]
	scheme := ""
	if isAuth {
		scheme = authSchemeRe.FindString(rest)
	}
	if strings.HasPrefix(rest, "[REDACTED") || strings.HasPrefix(rest[len(scheme):], "[REDACTED") {
		return "", 0, false
	}
	end := quotedEnd(s, start, level)
	v := s[start:end]
	if scheme != "" && len(v)-len(scheme) >= minSecretLen {
		return scheme + secretMark, end, true
	}
	if len(v) < minSecretLen {
		return "", 0, false
	}
	return secretMark, end, true
}

// redactBare does the same for an unquoted value at the start of rest, and also
// returns how much of rest the replacement stands for.
func redactBare(rest string, isAuth bool) (string, int, bool) {
	if isAuth {
		if scheme := authSchemeRe.FindString(rest); scheme != "" {
			cred := bareValueRe.FindString(rest[len(scheme):])
			if strings.HasPrefix(cred, "[") {
				return "", 0, false
			}
			if len(cred) >= minSecretLen {
				return scheme + secretMark, len(scheme) + len(cred), true
			}
		}
	}
	// A leading "[" is a list or an earlier redaction, not a secret.
	v := bareValueRe.FindString(rest)
	if len(v) < minSecretLen || strings.HasPrefix(v, "[") {
		return "", 0, false
	}
	return secretMark, len(v), true
}

// quoteLevel says how deeply nested a quote preceded by a run of backslashes is: a
// bare quote is level 0, \" is a quote inside one JSON string (1), \\\" a quote inside
// a string inside a string (3), and so on. An even run is escaped backslashes followed
// by a bare quote; in general the level is set by the trailing one bits of the run.
func quoteLevel(run int) int {
	level := 0
	for run&1 == 1 {
		level = level<<1 | 1
		run >>= 1
	}
	return level
}

// quotedEnd returns where a quoted value starting at start ends: at the quote of the
// same level that closes it, or earlier if the string containing it ends first (a
// quote of a lower level), or at a raw line break, which no JSON string contains.
// Quotes of a higher level are escaped quotes inside the value. The backslashes that
// belong to the value stay in it; the ones that belong to the closing quote do not.
func quotedEnd(s string, start, level int) int {
	i := start
	for i < len(s) {
		switch s[i] {
		case '\n', '\r':
			return i
		case '\\', '"':
			run := 0
			for i+run < len(s) && s[i+run] == '\\' {
				run++
			}
			if i+run >= len(s) {
				// Trailing backslashes: keep an unfinished escape out of the value.
				return i + run - run%2
			}
			if s[i+run] != '"' {
				i += run // \n, é: an escape inside the value
				continue
			}
			switch q := quoteLevel(run); {
			case q < level:
				return i
			case q == level:
				return i + run - level
			}
			i += run + 1
		default:
			i++
		}
	}
	return len(s)
}

// closesString reports whether what follows a quote is what follows the end of a JSON
// string: a delimiter, or nothing.
func closesString(rest string) bool {
	rest = strings.TrimLeft(rest, " \t\r\n")
	return rest == "" || strings.ContainsRune(",}]:", rune(rest[0]))
}

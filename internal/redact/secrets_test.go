package redact

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
)

// jsonDocs are valid JSON frames with a secret somewhere inside. Redaction runs over
// raw frames, so whatever it does to them, they have to stay parseable.
var jsonDocs = []struct {
	doc     string
	secrets []string
}{
	{`{"command":"echo \"password=hunter22\""}`, []string{"hunter22"}},
	{`{"command":"curl -d \"token=abcdef123456\" https://x"}`, []string{"abcdef123456"}},
	{`{"content":[{"type":"text","text":"{\"password\":\"hunter2hunter2\"}"}]}`, []string{"hunter2hunter2"}},
	{`{"a":"{\"b\":\"{\\\"password\\\":\\\"hunter2hunter2\\\"}\"}"}`, []string{"hunter2hunter2"}},
	{`{"password":"correct horse battery staple"}`, []string{"correct", "horse", "staple"}},
	{`{"password":"my dog has fleas"}`, []string{"fleas"}},
	{`{"password":"with \"quotes\" inside it"}`, []string{"quotes", "inside"}},
	{`{"password":"ends with a backslash\\"}`, []string{"backslash"}},
	{`{"text":"\"password\":\"abcdefgh\\\\\""}`, []string{"abcdefgh"}},
	{`{"text":"\"password\":\"abcdefgh\\"}`, []string{"abcdefgh"}},
	{`{"text":"password=hunter22\\"}`, []string{"hunter22"}},
	{`{"token":1234567890}`, []string{"1234567890"}},
	{`{"n":1,"secret":-12345.678,"m":2}`, []string{"12345.678"}},
	{`{"env":"password=hunter22\nuser=bob"}`, []string{"hunter22"}},
	{`{"env":"password=caf\u00e9-au-lait\tx"}`, []string{"au-lait"}},
	{`{"headers":{"Authorization":"Basic dXNlcjpodW50ZXIyaHVudGVyMg=="}}`, []string{"dXNlcjpodW50ZXIyaHVudGVyMg"}},
	{`{"text":"Authorization: Basic dXNlcjpodW50ZXIyaHVudGVyMg=="}`, []string{"dXNlcjpodW50ZXIyaHVudGVyMg"}},
	{`{"text":"Authorization: Bearer abc123def456"}`, []string{"abc123def456"}},
	{`{"text":"curl -H \"Authorization: Bearer abc123def456\" https://x"}`, []string{"abc123def456"}},
	{`{"url":"redis://:s3cretpassw0rd@cache:6379/0"}`, []string{"s3cretpassw0rd"}},
	{`{"url":"postgres://app:hunter\\\\22@db/prod"}`, []string{"hunter"}},
	{`{"key":"-----BEGIN RSA PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSj\nAgEAAoIBAQC7VJTUt9Us8cKjMzEfYyjiWA4R"}`, []string{"MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSj", "AgEAAoIBAQC7VJTUt9Us8cKjMzEfYyjiWA4R"}},
	{`{"aws":"aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}`, []string{"wJalrXUtnFEMI"}},
	// No secret here: a string that merely ends in a key name must not make the
	// rule read the neighbouring JSON as its value.
	{`["token:",123456,789012,"x"]`, nil},
	{`{"a":"my password:","b":[1234567,2345678],"c":"x"}`, nil},
	{`{"text":"\"token\":","nums":[1234567,2345678],"z":"y"}`, nil},
	{`{"text":"\"a\":\"password=\",\"b\":[1234567,2345678]","z":"y"}`, nil},
	{`{"text":"he said \"my password:\"","next":"value here"}`, nil},
}

func TestRedactionKeepsValidJSONValid(t *testing.T) {
	r := Default()
	for _, c := range jsonDocs {
		if !json.Valid([]byte(c.doc)) {
			t.Fatalf("test document is not valid JSON: %s", c.doc)
		}
		out := r.String(c.doc)
		if !json.Valid([]byte(out)) {
			t.Errorf("redaction broke JSON:\n in  %s\n out %s", c.doc, out)
		}
		for _, s := range c.secrets {
			if strings.Contains(out, s) {
				t.Errorf("secret %q survived:\n in  %s\n out %s", s, c.doc, out)
			}
		}
		if len(c.secrets) == 0 && out != c.doc {
			t.Errorf("nothing to redact, but the document changed:\n in  %s\n out %s", c.doc, out)
		}
		if twice := r.String(out); twice != out {
			t.Errorf("second pass changed the output:\n once  %s\n twice %s", out, twice)
		}
	}
}

func FuzzRedactKeepsJSONValid(f *testing.F) {
	for _, c := range jsonDocs {
		f.Add(c.doc)
	}
	f.Add(`{"k":"-----BEGIN PRIVATE KEY-----","b":{"c":"-----END PRIVATE KEY-----"}}`)
	r := Default()
	f.Fuzz(func(t *testing.T, in string) {
		if !json.Valid([]byte(in)) {
			return
		}
		if out := r.String(in); !json.Valid([]byte(out)) {
			t.Fatalf("redaction broke JSON:\n in  %s\n out %s", in, out)
		}
	})
}

// The same property for text wrapped in JSON one, two and three times, which is how
// tool results carry command lines, config files and other tools' JSON.
func TestRedactionSurvivesNestedEncoding(t *testing.T) {
	r := Default()
	texts := []struct{ text, secret string }{
		{`password=hunter22`, "hunter22"},
		{`echo "password=hunter22"`, "hunter22"},
		{`{"password":"hunter2hunter2"}`, "hunter2hunter2"},
		{`{"password":"my dog has fleas"}`, "fleas"},
		{`{"password":"back\\slash \"quoted\" value"}`, "quoted"},
		{`export API_KEY='abcdef123456'`, "abcdef123456"},
		{"token: abcdef123456\nuser: bob", "abcdef123456"},
		{`Authorization: Basic dXNlcjpodW50ZXIyaHVudGVyMg==`, "dXNlcjpodW50ZXIyaHVudGVyMg"},
		{`redis://:s3cretpassw0rd@cache:6379/0`, "s3cretpassw0rd"},
		{`C:\Users\me> set PASSWORD=hunter22`, "hunter22"},
	}
	for _, c := range texts {
		doc := c.text
		for depth := 1; depth <= 3; depth++ {
			b, err := json.Marshal(map[string]string{"text": doc})
			if err != nil {
				t.Fatal(err)
			}
			doc = string(b)
			out := r.String(doc)
			if !json.Valid([]byte(out)) {
				t.Errorf("depth %d: redaction broke JSON:\n in  %s\n out %s", depth, doc, out)
			}
			if strings.Contains(out, c.secret) {
				t.Errorf("depth %d: secret %q survived:\n in  %s\n out %s", depth, c.secret, doc, out)
			}
			if twice := r.String(out); twice != out {
				t.Errorf("depth %d: second pass changed the output:\n once  %s\n twice %s", depth, out, twice)
			}
		}
	}
}

// Random documents built from the pieces the rules react to: key names, separators,
// quotes, backslashes, markers. Byte-level fuzzing rarely produces valid JSON, so this
// is what exercises the quoting logic. The seed is fixed; a failure is reproducible.
func TestRedactionOnGeneratedJSON(t *testing.T) {
	pieces := []string{
		"password", "token", "Authorization", "secret_access_key", "x", "=", ":", ": ", " ",
		`"`, `\`, ",", "[", "]", "{", "}", "\n", "\t", "abcdefgh", "1234567", "Basic ", "Bearer ",
		"-----BEGIN PRIVATE KEY-----", "-----END PRIVATE KEY-----", "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSj",
		"redis://:", "db://user:", "@host", "[REDACTED:secret]", "é", "sk-abcdefghijklmnopqrstuvwxyz",
	}
	rng := rand.New(rand.NewSource(1))
	text := func() string {
		var b strings.Builder
		for n := rng.Intn(8); n > 0; n-- {
			b.WriteString(pieces[rng.Intn(len(pieces))])
		}
		return b.String()
	}
	var value func(depth int) any
	value = func(depth int) any {
		switch k := rng.Intn(8); {
		case depth > 2 || k < 3:
			return text()
		case k == 3:
			return rng.Intn(100000000)
		case k == 4:
			// JSON carried as a string, the way tool results carry it.
			b, _ := json.Marshal(value(depth + 1))
			return string(b)
		case k == 5:
			list := make([]any, rng.Intn(4))
			for i := range list {
				list[i] = value(depth + 1)
			}
			return list
		default:
			obj := map[string]any{}
			for n := rng.Intn(4); n > 0; n-- {
				obj[text()] = value(depth + 1)
			}
			return obj
		}
	}

	r := Default()
	for i := 0; i < 20000; i++ {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(value(0)); err != nil {
			t.Fatal(err)
		}
		in := strings.TrimSuffix(buf.String(), "\n")
		out := r.String(in)
		if !json.Valid([]byte(out)) {
			t.Fatalf("redaction broke JSON:\n in  %s\n out %s", in, out)
		}
		if twice := r.String(out); twice != out {
			t.Fatalf("second pass changed the output:\n in    %s\n once  %s\n twice %s", in, out, twice)
		}
	}
}

func TestSecretAssignmentRedactsTheWholeValue(t *testing.T) {
	r := Default()
	cases := []struct{ in, want string }{
		// A quoted value ends at its closing quote, not at the first space.
		{`{"password":"correct horse battery staple"}`, `{"password":"[REDACTED:secret]"}`},
		{`{"password":"my dog has fleas"}`, `{"password":"[REDACTED:secret]"}`},
		{`password="my dog has fleas" next`, `password="[REDACTED:secret]" next`},
		// Escaped quotes: JSON carried inside a JSON string, and shell quoting.
		{`{"content":[{"type":"text","text":"{\"password\":\"hunter2hunter2\"}"}]}`,
			`{"content":[{"type":"text","text":"{\"password\":\"[REDACTED:secret]\"}"}]}`},
		{`{"command":"echo \"password=hunter22\""}`, `{"command":"echo \"password=[REDACTED:secret]\""}`},
		{`{"command":"curl -d \"token=abcdef123456\" https://x"}`,
			`{"command":"curl -d \"token=[REDACTED:secret]\" https://x"}`},
		// An unquoted value ends at an escaped line break, and leaves what follows.
		{`{"env":"password=hunter22\nuser=bob"}`, `{"env":"password=[REDACTED:secret]\nuser=bob"}`},
		// A bare number has to be replaced by a string to stay JSON.
		{`{"token":1234567890}`, `{"token":"[REDACTED:secret]"}`},
		// Authorization keeps the scheme and drops the credentials.
		{`Authorization: Bearer abc123def456`, `Authorization: Bearer [REDACTED:secret]`},
		{`Authorization: Basic dXNlcjpodW50ZXIyaHVudGVyMg==`, `Authorization: Basic [REDACTED:secret]`},
		{`Authorization: Bearer abcdefghijklmnopqrstuvwxyz`, `Authorization: Bearer [REDACTED:bearer-token]`},
		{`{"Authorization":"Basic dXNlcjpodW50ZXIy"}`, `{"Authorization":"Basic [REDACTED:secret]"}`},
		{`{"authorization":"dXNlcjpodW50ZXIy"}`, `{"authorization":"[REDACTED:secret]"}`},
		{`authorization=abcdef123456 followed by words`, `authorization=[REDACTED:secret] followed by words`},
		// AWS secret access keys have no shape of their own, only a name.
		{`aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`, `aws_secret_access_key = [REDACTED:secret]`},
		{`{"SecretAccessKey":"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}`, `{"SecretAccessKey":"[REDACTED:secret]"}`},
	}
	for _, c := range cases {
		if got := r.String(c.in); got != c.want {
			t.Errorf("redacting %s\n got  %s\n want %s", c.in, got, c.want)
		}
		if !r.HasSecrets(c.in) {
			t.Errorf("HasSecrets missed %s", c.in)
		}
	}

	unchanged := []string{
		`{"password":"abc"}`, // too short to be worth a marker
		`{"input_tokens":1200,"output_tokens":300,"max_tokens":4096}`,
		`{"password":"[REDACTED:secret]"}`,
		`Authorization: Basic [REDACTED:secret]`,
		`["token:",123456,789012,"x"]`,
		`{"properties":{"password":{"type":"string","description":"the account password"}}}`,
	}
	for _, in := range unchanged {
		if got := r.String(in); got != in {
			t.Errorf("should be left alone: %s\n got %s", in, got)
		}
		if r.HasSecrets(in) {
			t.Errorf("HasSecrets flagged %s", in)
		}
	}
}

func TestURLPasswordWithEmptyUser(t *testing.T) {
	got := Default().String(`redis://:s3cretpassw0rd@cache:6379/0`)
	if want := `redis://:[REDACTED:password]@cache:6379/0`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	// A port is not a password.
	for _, in := range []string{`http://localhost:8080/a@b`, `see http://:8080/ and mail me@example.com`} {
		if got := Default().String(in); got != in {
			t.Errorf("%s changed to %s", in, got)
		}
	}
	// A marker left by a later rule is not a password on the next pass.
	once := Default().String(`a://u:token:db://user:x@host`)
	if twice := Default().String(once); twice != once {
		t.Errorf("second pass changed the output:\n once  %s\n twice %s", once, twice)
	}
}

func TestPrivateKeys(t *testing.T) {
	const body = "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj\nMzEfYyjiWA4R4/M2bS1GB4t7NXp98C3SC6dVMvDuictGeurT8jNbvJZHtCSuYEvu"
	cases := []struct{ name, in, want string }{
		{"complete", "-----BEGIN RSA PRIVATE KEY-----\n" + body + "\n-----END RSA PRIVATE KEY-----\nafter",
			"[REDACTED:private-key]\nafter"},
		// Truncated output has no END marker; the base64 block still has to go.
		{"truncated", "key:\n-----BEGIN PRIVATE KEY-----\n" + body, "key:\n[REDACTED:private-key]"},
		{"truncated, then text", "-----BEGIN OPENSSH PRIVATE KEY-----\n" + body + "\n\nand then some ordinary words",
			"[REDACTED:private-key]\n\nand then some ordinary words"},
		{"truncated in JSON", `{"k":"-----BEGIN EC PRIVATE KEY-----\n` + strings.ReplaceAll(body, "\n", `\n`) + `"}`,
			`{"k":"[REDACTED:private-key]"}`},
		{"encrypted, truncated", "-----BEGIN RSA PRIVATE KEY-----\nProc-Type: 4,ENCRYPTED\nDEK-Info: AES-256-CBC,0123456789ABCDEF\n\n" + body,
			"[REDACTED:private-key]"},
		{"pgp", "-----BEGIN PGP PRIVATE KEY BLOCK-----\nVersion: GnuPG v2\n\n" + body + "\n=AbCd\n-----END PGP PRIVATE KEY BLOCK-----",
			"[REDACTED:private-key]"},
		{"pgp, truncated", "-----BEGIN PGP PRIVATE KEY BLOCK-----\nVersion: GnuPG v2\n\n" + body,
			"[REDACTED:private-key]"},
	}
	r := Default()
	for _, c := range cases {
		if got := r.String(c.in); got != c.want {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
	}
	// Public material is not a secret.
	pub := "-----BEGIN PUBLIC KEY-----\n" + body + "\n-----END PUBLIC KEY-----"
	if got := r.String(pub); got != pub {
		t.Errorf("public key was redacted: %q", got)
	}
}

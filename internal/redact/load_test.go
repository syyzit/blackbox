package redact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fake credentials are assembled at run time so the source never contains a string
// that secret scanners would flag.
func fake(prefix string, n int) string { return prefix + strings.Repeat("a1B2", n/4+1)[:n] }

func TestMoreCredentialShapes(t *testing.T) {
	r := Default()
	cases := map[string]string{
		"github-fine-grained": fake("github"+"_pat_", 40),
		"gitlab":              fake("gl"+"pat-", 20),
		"google":              fake("AI"+"za", 35),
		"stripe":              fake("sk"+"_live_", 24),
		"stripe-restricted":   fake("rk"+"_test_", 24),
		"slack":               fake("xo"+"xb-", 24),
		"slack-webhook":       "https://hooks.slack.com/services/" + fake("T0/B0/", 24),
		"npm":                 fake("np"+"m_", 36),
		"huggingface":         fake("h"+"f_", 34),
	}
	for name, secret := range cases {
		in := `{"value":"` + secret + `"}`
		out := r.String(in)
		if strings.Contains(out, secret) || !strings.Contains(out, "[REDACTED:") {
			t.Errorf("%s: not redacted: %s", name, out)
		}
	}
}

func TestURLCredentialsKeepTheRestOfTheURL(t *testing.T) {
	out := Default().String(`DATABASE_URL=postgres://app:` + fake("pw", 12) + `@db.internal:5432/prod`)
	want := `postgres://app:[REDACTED:password]@db.internal:5432/prod`
	if !strings.Contains(out, want) {
		t.Fatalf("got %s, want it to contain %s", out, want)
	}
	// A port is not a password.
	plain := `see http://example.com:8080/path@v2 for details`
	if got := Default().String(plain); got != plain {
		t.Fatalf("URL without credentials was changed: %s", got)
	}
}

func TestSecretAssignmentCoversMoreKeys(t *testing.T) {
	r := Default()
	for _, in := range []string{
		`{"token":"` + fake("t", 20) + `"}`,
		`{"private_key":"` + fake("k", 20) + `"}`,
		`credentials=` + fake("c", 20),
	} {
		if out := r.String(in); !strings.Contains(out, "[REDACTED:secret]") {
			t.Errorf("not redacted: %s", out)
		}
	}
	// Token counts in usage reports stay readable.
	usage := `{"input_tokens":1200,"output_tokens":300,"max_tokens":4096}`
	if got := r.String(usage); got != usage {
		t.Fatalf("usage numbers were redacted: %s", got)
	}
}

func TestLoadAddsUserRules(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "redact.json")

	r, err := Load(path) // missing file: defaults only
	if err != nil || len(r.Rules()) != len(defaultRules) {
		t.Fatalf("missing file: %d rules, %v", len(r.Rules()), err)
	}

	os.WriteFile(path, []byte(`{"rules":[
		{"name":"internal-token","pattern":"itk_[A-Za-z0-9]{8}"},
		{"pattern":"(cust_)[0-9]{6}","replace":"${1}[REDACTED]"}
	]}`), 0o600)
	r, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	out := r.String(`token itk_abcd1234 for cust_123456, key ` + fake("sk-", 20))
	for _, want := range []string{"[REDACTED:internal-token]", "cust_[REDACTED]", "[REDACTED:openai-key]"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
}

func TestLoadRejectsBadRules(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"syntax":  `{"rules":[`,
		"regex":   `{"rules":[{"pattern":"("}]}`,
		"empty":   `{"rules":[{"name":"x"}]}`,
		"matches": `{"rules":[{"pattern":"a*"}]}`,
	} {
		path := filepath.Join(dir, name+".json")
		os.WriteFile(path, []byte(body), 0o600)
		if _, err := Load(path); err == nil {
			t.Errorf("%s: invalid redact.json was accepted", name)
		}
	}
}

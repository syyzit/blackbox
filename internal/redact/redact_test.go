package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDefaultRedactsCredentialShapes(t *testing.T) {
	r := Default()

	cases := map[string]string{
		"openai":    `{"key":"sk-abcdefghijklmnopqrstuvwxyz123456"}`,
		"anthropic": `{"key":"sk-ant-api03-abcdefghijklmnopqrstuvwxyz"}`,
		"github":    `{"token":"ghp_abcdefghijklmnopqrstuvwxyz012345"}`,
		"aws":       `{"id":"AKIAIOSFODNN7EXAMPLE"}`,
		"bearer":    `Authorization: Bearer abcdefghijklmnopqrstuvwxyz`,
		"assign":    `{"password":"hunter2hunter2"}`,
	}
	for name, in := range cases {
		out := r.String(in)
		if out == in {
			t.Errorf("%s: payload was not redacted: %s", name, out)
		}
		if !strings.Contains(out, "[REDACTED:") {
			t.Errorf("%s: missing redaction marker: %s", name, out)
		}
	}
}

func TestAnthropicKeysGetTheirOwnLabel(t *testing.T) {
	out := Default().String(`{"key":"sk-ant-api03-abcdefghijklmnopqrstuvwxyz"}`)
	if !strings.Contains(out, "[REDACTED:anthropic-key]") {
		t.Fatalf("anthropic key labelled wrongly: %s", out)
	}
}

func TestRedactionKeepsJSONValid(t *testing.T) {
	r := Default()
	in := `{"api_key":"supersecretvalue","text":"keep me","n":3}`
	out := r.String(in)
	if !json.Valid([]byte(out)) {
		t.Fatalf("redaction broke JSON: %s", out)
	}
	if strings.Contains(out, "supersecretvalue") {
		t.Fatalf("secret survived: %s", out)
	}
	if !strings.Contains(out, "keep me") {
		t.Fatalf("redaction was too aggressive: %s", out)
	}
}

func TestRedactionLeavesOrdinaryTextAlone(t *testing.T) {
	r := Default()
	in := `{"text":"the quick brown fox jumps over the lazy dog"}`
	if got := r.String(in); got != in {
		t.Fatalf("ordinary text changed:\n got: %s\nwant: %s", got, in)
	}
}

func TestNilRedactorIsPassthrough(t *testing.T) {
	var r *Redactor
	if got := r.String("sk-abcdefghijklmnopqrstuvwxyz123456"); !strings.HasPrefix(got, "sk-") {
		t.Fatalf("nil redactor should be a passthrough, got %q", got)
	}
}

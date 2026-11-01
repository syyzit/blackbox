package redact

import (
	"strings"
	"testing"
)

func TestRedactionIsIdempotent(t *testing.T) {
	r := Default()
	for _, in := range []string{
		`{"password":"hunter2hunter2"}`,
		`Authorization: Bearer abcdefghijklmnopqrstuvwxyz`,
		`api_key=sk-abcdefghijklmnopqrstuvwxyz123456`,
	} {
		once := r.String(in)
		if twice := r.String(once); twice != once {
			t.Errorf("second pass changed the output:\n once  %s\n twice %s", once, twice)
		}
	}
}

func FuzzRedact(f *testing.F) {
	for _, s := range []string{
		`{"password":"hunter2hunter2"}`,
		`Authorization: Bearer abcdefghijklmnopqrstuvwxyz`,
		`{"key":"sk-ant-api03-abcdefghijklmnopqrstuvwxyz"}`,
		`-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----`,
		`eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U`,
	} {
		f.Add(s)
	}
	r := Default()
	f.Fuzz(func(t *testing.T, in string) {
		once := r.String(in)
		if twice := r.String(once); twice != once {
			t.Fatalf("redaction is not idempotent:\n in    %q\n once  %q\n twice %q", in, once, twice)
		}
		// Known credential shapes never survive a pass.
		for _, rule := range r.Rules() {
			if rule.Name == "secret-assignment" || rule.Name == "bearer-token" {
				continue // these keep a prefix, so their pattern can match the output
			}
			if loc := rule.Re.FindStringIndex(once); loc != nil && !strings.HasPrefix(once[loc[0]:], "[REDACTED") {
				t.Fatalf("%s survived redaction in %q", rule.Name, once)
			}
		}
	})
}

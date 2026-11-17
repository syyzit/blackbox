package proxy

import (
	"strings"
	"testing"

	"github.com/syyzit/blackbox/internal/event"
	"github.com/syyzit/blackbox/internal/redact"
)

// Only a prefix of an oversized frame is recorded. A secret cut in half by that prefix
// no longer matches its redaction pattern, so the half that is kept must not be
// recorded as it stands.
func TestOversizePrefixNeverKeepsPartOfASecret(t *testing.T) {
	secrets := []struct{ name, before, secret string }{
		{"api key", `"text":"`, "sk-SECRETabcdefghijklmnopqrstuvwxyz0123456789"},
		{"quoted password", `"password":"`, "SECRET horse battery staple"},
		{"url password", `"url":"postgres://app:`, "SECRETpassw0rd@db:5432/prod"},
		{"bearer", `"auth":"Bearer `, "SECRETabcdefghijklmnopqrstuvwxyz"},
	}
	for _, s := range secrets {
		// Cut the secret at every point that leaves part of it inside the prefix.
		for keep := 1; keep < len(s.secret); keep++ {
			pad := recordedPrefix - len(s.before) - keep - len(`{"x":"`)
			frame := `{"x":"` + strings.Repeat("a", pad) + s.before + s.secret + `"}` + strings.Repeat(" ", 100)

			sink := &corrSink{}
			p := newPipeline(pipelineConfig{SessionID: "s", Sink: sink, Redactor: redact.Default()})
			p.recordOversize(event.ClientToServer, []byte(frame), len(frame), "test", nil)
			raw := sink.events[0].Raw
			if strings.Contains(raw, "SECRET") || strings.HasSuffix(strings.TrimSuffix(raw, "…"), s.secret[:keep]) {
				t.Fatalf("%s cut after %d bytes: part of the secret was recorded: …%s", s.name, keep, raw[len(raw)-80:])
			}
			if len(raw) > recordedPrefix+len("…") {
				t.Fatalf("%s: prefix grew to %d bytes", s.name, len(raw))
			}
		}
	}
}

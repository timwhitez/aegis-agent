package output

import (
	"bytes"
	"testing"

	"aegis-agent/internal/events"
)

func TestRendererShowsSteerAndContinueHints(t *testing.T) {
	var out bytes.Buffer
	renderer := New(false, &out)

	renderer.Handle(events.New("s1", "session.started", "prepare", nil))
	renderer.Handle(events.New("s1", "session.awaiting_input", "turn_decide", nil))
	renderer.Handle(events.New("s1", "session.paused", "interrupt", nil))
	renderer.Handle(events.New("s1", "session.failed", "error", nil))

	text := out.String()
	for _, needle := range []string{
		`steer: aegis-agent steer s1 --message "..."`,
		`next: aegis-agent continue s1 --message "..."`,
	} {
		if !bytes.Contains([]byte(text), []byte(needle)) {
			t.Fatalf("expected %q in renderer output, got %s", needle, text)
		}
	}
}

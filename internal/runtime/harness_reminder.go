package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"go-cli-agent/internal/session"
)

const harnessReminderSignatureKey = "signature"

func harnessReminderTextSignature(text string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(sum[:])
}

func harnessReminderExists(store *session.Store, sessionID, kind, signature, text string) (bool, error) {
	kind = strings.TrimSpace(kind)
	signature = strings.TrimSpace(signature)
	text = strings.TrimSpace(text)
	if kind == "" {
		return false, nil
	}
	found := false
	err := store.VisitMessages(sessionID, func(msg session.Message) error {
		if found || msg.Meta == nil {
			return nil
		}
		source, _ := msg.Meta["source"].(string)
		msgKind, _ := msg.Meta["kind"].(string)
		if source != "harness_reminder" || msgKind != kind {
			return nil
		}
		if signature != "" {
			msgSignature, _ := msg.Meta[harnessReminderSignatureKey].(string)
			if strings.TrimSpace(msgSignature) == signature {
				found = true
				return nil
			}
		}
		// Backward compatibility for reminders written before signatures existed.
		if text != "" && strings.TrimSpace(msg.Text) == text {
			found = true
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return found, nil
}

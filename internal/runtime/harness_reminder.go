package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"go-cli-agent/internal/session"
)

const harnessReminderSignatureKey = "signature"
const harnessReminderFallbackTailLimit = 256

func harnessReminderTextSignature(text string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(sum[:])
}

func harnessReminderSemanticSignature(kind string, childSessions, queueJobs []string) string {
	children := normalizedReminderItems(childSessions)
	jobs := normalizedReminderItems(queueJobs)
	return harnessReminderTextSignature(strings.TrimSpace(kind) +
		"\nchildren:" + strings.Join(children, "\x00") +
		"\njobs:" + strings.Join(jobs, "\x00"))
}

func normalizedReminderItems(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	sort.Strings(out)
	if len(out) < 2 {
		return out
	}
	unique := out[:0]
	for _, item := range out {
		if len(unique) == 0 || unique[len(unique)-1] != item {
			unique = append(unique, item)
		}
	}
	return unique
}

func harnessReminderExists(store *session.Store, sessionID, kind, signature, text string) (bool, error) {
	kind = strings.TrimSpace(kind)
	signature = strings.TrimSpace(signature)
	text = strings.TrimSpace(text)
	if kind == "" {
		return false, nil
	}
	if signature != "" {
		recorded, err := store.HarnessReminderRecorded(sessionID, kind, signature)
		if err != nil {
			return false, err
		}
		if recorded {
			return true, nil
		}
	}
	messages, _, err := store.LoadMessagesTail(sessionID, harnessReminderFallbackTailLimit)
	if err != nil {
		return false, err
	}
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Meta == nil {
			continue
		}
		source, _ := msg.Meta["source"].(string)
		msgKind, _ := msg.Meta["kind"].(string)
		if source != "harness_reminder" || msgKind != kind {
			continue
		}
		if signature != "" {
			msgSignature, _ := msg.Meta[harnessReminderSignatureKey].(string)
			if strings.TrimSpace(msgSignature) == signature {
				_ = store.RecordHarnessReminder(sessionID, kind, signature, msg.ID)
				return true, nil
			}
		}
		// Backward compatibility for reminders written before signatures existed.
		if text != "" && strings.TrimSpace(msg.Text) == text {
			_ = store.RecordHarnessReminder(sessionID, kind, signature, msg.ID)
			return true, nil
		}
	}
	return false, nil
}

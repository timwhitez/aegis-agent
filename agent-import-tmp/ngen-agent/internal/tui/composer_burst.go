package tui

import (
	"time"
	"unicode"
)

const (
	composerBurstCharInterval           = 8 * time.Millisecond
	composerBurstEnterSuppressionWindow = 120 * time.Millisecond
	composerBurstMinASCIIChars          = 3
	composerBurstDirectASCIICharsMin    = 3
	composerBurstNonASCIICharsMin       = 16
)

// composerBurst tracks the minimal burst state NGEN needs around the textarea:
//
//   - hold short rapid ASCII sequences until we know whether Enter should stay a
//     newline instead of becoming submit,
//   - keep a brief Enter suppression window after a paste-like burst flushes, and
//   - avoid treating short non-ASCII / IME commits as paste unless they look
//     obviously paste-like.
type composerBurst struct {
	lastASCIIAt        time.Time
	enterSuppressionTo time.Time
	pendingASCII       []rune
}

func (b *composerBurst) ObserveASCII(now time.Time, r rune) {
	if b.lastASCIIAt.IsZero() || now.Sub(b.lastASCIIAt) > composerBurstCharInterval {
		b.pendingASCII = b.pendingASCII[:0]
	}
	b.lastASCIIAt = now
	b.pendingASCII = append(b.pendingASCII, r)
	if len(b.pendingASCII) >= composerBurstMinASCIIChars {
		b.enterSuppressionTo = now.Add(composerBurstEnterSuppressionWindow)
	}
}

func (b *composerBurst) ObserveDirectRunes(now time.Time, runes []rune) {
	if looksDirectPasteLike(runes) {
		b.enterSuppressionTo = now.Add(composerBurstEnterSuppressionWindow)
	}
}

func (b composerBurst) ShouldInsertNewline(now time.Time, slashContext bool) bool {
	if slashContext {
		return false
	}
	if len(b.pendingASCII) >= composerBurstMinASCIIChars &&
		!b.lastASCIIAt.IsZero() &&
		now.Sub(b.lastASCIIAt) <= composerBurstCharInterval {
		return true
	}
	if b.enterSuppressionTo.IsZero() {
		return false
	}
	return !now.After(b.enterSuppressionTo)
}

func (b *composerBurst) Extend(now time.Time) {
	if b.enterSuppressionTo.IsZero() {
		return
	}
	b.enterSuppressionTo = now.Add(composerBurstEnterSuppressionWindow)
}

func (b *composerBurst) FlushDue(now time.Time) string {
	if len(b.pendingASCII) == 0 || b.lastASCIIAt.IsZero() {
		return ""
	}
	if now.Sub(b.lastASCIIAt) <= composerBurstCharInterval {
		return ""
	}
	return b.FlushNow()
}

func (b *composerBurst) FlushNow() string {
	if len(b.pendingASCII) == 0 {
		return ""
	}
	text := string(b.pendingASCII)
	b.pendingASCII = b.pendingASCII[:0]
	b.lastASCIIAt = time.Time{}
	return text
}

func (b composerBurst) PendingText() string {
	return string(b.pendingASCII)
}

func (b composerBurst) HasPendingText() bool {
	return len(b.pendingASCII) > 0
}

func (b *composerBurst) Clear() {
	*b = composerBurst{}
}

func composerBurstFlushDelay() time.Duration {
	return composerBurstCharInterval + time.Millisecond
}

func looksDirectPasteLike(runes []rune) bool {
	if len(runes) == 0 {
		return false
	}
	containsWhitespace := false
	containsNonASCII := false
	for _, r := range runes {
		if unicode.IsSpace(r) {
			containsWhitespace = true
		}
		if r > unicode.MaxASCII {
			containsNonASCII = true
		}
	}
	if containsNonASCII {
		if containsWhitespace {
			return len(runes) >= composerBurstMinASCIIChars
		}
		return len(runes) >= composerBurstNonASCIICharsMin
	}
	if containsWhitespace {
		return len(runes) >= composerBurstMinASCIIChars
	}
	return len(runes) >= composerBurstDirectASCIICharsMin
}

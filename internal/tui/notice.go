package tui

import "time"

// DefaultNoticeTTL is how long a transient status line lives before it
// clears itself. A notice confirms *your* last action — a toggle, a save, a
// "request sent" — so once you have seen it, keeping it is how "context
// compacted" goes stale underneath the next turn. Eight seconds is long
// enough to read on a glance back, short enough to never describe a previous
// turn. Configurable per model via display.notice_ttl (seconds); zero or
// negative falls back to this.
//
// The sort between the status line and the transcript is durability:
//
//   - Inline block (durable, scrolls with the record): errors, warnings
//     about the world or the model, anything that explains transcript
//     content. Rule of thumb: would scrollback lose meaning without it?
//     Precedent: EventError → BlockError, warning+ EventNotice →
//     BlockNotice, memory recall → BlockMemory, compaction →
//     BlockCompacted (applyEvent in app.go).
//   - Status line (transient, expires): confirmations of your own command.
//     Rule of thumb: if you looked away for ten seconds, did you miss
//     anything? If not, it is a notice, and it expires.
//
// The expiry is lazy so the ~300 existing `m.notice = …` sites need no
// changes: the first tick that sees a new text stamps it, and a later tick
// clears it past the TTL. Pinned notices (detach confirm, masked-input
// prompts) opt out via setPinnedNotice; they clear on replace or submit
// like everything else.
const DefaultNoticeTTL = 8 * time.Second

// effectiveNoticeTTL reports the configured TTL, or the default when unset.
func (m *Model) effectiveNoticeTTL() time.Duration {
	if m.noticeTTL > 0 {
		return m.noticeTTL
	}
	return DefaultNoticeTTL
}

// setNotice shows a transient status line, replacing any pinned one.
func (m *Model) setNotice(s string) {
	m.notice = s
	m.noticePin, m.noticePinText = false, ""
}

// setPinnedNotice shows a status line that survives expiry. For prompts that
// must outlive a glance: the two-press detach confirm (the armed state
// persists until Esc, typing, or submit) and masked-input prompts (they
// describe the input row itself, not a past action).
func (m *Model) setPinnedNotice(s string) {
	m.notice = s
	m.noticePin, m.noticePinText = true, s
}

// expireNotice clears a transient notice older than the effective TTL. Pinned notices
// and empty rows only reset the bookkeeping. Pure on (notice, pin, now) apart
// from the mutation, so tests drive it with fake clocks; the tick handler
// skips it under Deterministic like every other wall-clock read.
func (m *Model) expireNotice(now time.Time) {
	if m.notice == "" {
		m.noticeSeen, m.noticePin, m.noticePinText = "", false, ""
		return
	}
	if m.noticePin && m.notice == m.noticePinText {
		m.noticeSeen = m.notice
		return
	}
	if m.notice != m.noticeSeen {
		m.noticeSeen, m.noticeSeenAt = m.notice, now
		return
	}
	if now.Sub(m.noticeSeenAt) >= m.effectiveNoticeTTL() {
		m.notice, m.noticeSeen = "", ""
	}
}

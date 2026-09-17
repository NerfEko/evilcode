package tui

import (
	"testing"
	"time"

	"evilcode/internal/config"
)

func TestTransientNoticeExpiresAfterTTL(t *testing.T) {
	var m Model
	now := time.Now()
	m.notice = "✓ Context compacted. Retrying..."

	// First sighting stamps, not clears.
	m.expireNotice(now)
	if m.notice == "" {
		t.Fatal("a fresh notice cleared on first sighting")
	}
	// Still fresh just before the TTL.
	m.expireNotice(now.Add(DefaultNoticeTTL - time.Second))
	if m.notice == "" {
		t.Fatal("a fresh notice expired before DefaultNoticeTTL")
	}
	// Stale past the TTL.
	m.expireNotice(now.Add(DefaultNoticeTTL + time.Second))
	if m.notice != "" {
		t.Fatalf("stale notice survived past DefaultNoticeTTL: %q", m.notice)
	}
}

func TestReplacementNoticeRearmsTheCooldown(t *testing.T) {
	var m Model
	now := time.Now()
	m.notice = "first"
	m.expireNotice(now)
	m.notice = "second"
	m.expireNotice(now.Add(DefaultNoticeTTL + time.Second))
	if m.notice == "" {
		t.Fatal("a replacement notice inherited the old stamp and expired early")
	}
	m.expireNotice(now.Add(2*DefaultNoticeTTL + 2*time.Second))
	if m.notice != "" {
		t.Fatalf("replacement notice never expired: %q", m.notice)
	}
}

func TestPinnedNoticeSurvivesExpiryUntilReplaced(t *testing.T) {
	var m Model
	now := time.Now()
	m.setPinnedNotice("Press Ctrl+C again to detach")
	m.expireNotice(now.Add(10 * DefaultNoticeTTL))
	if m.notice == "" {
		t.Fatal("a pinned detach confirm expired while still armed")
	}
	// A transient replacement unpins: the next thing expires normally (first
	// sighting stamps the new text, a later tick clears it).
	m.setNotice("Saved")
	later := now.Add(20 * DefaultNoticeTTL)
	m.expireNotice(later)
	if m.notice == "" {
		t.Fatal("a replacement notice cleared on first sighting")
	}
	m.expireNotice(later.Add(DefaultNoticeTTL + time.Second))
	if m.notice != "" {
		t.Fatalf("transient replacement stayed pinned: %q", m.notice)
	}
}

func TestClearedNoticeResetsBookkeeping(t *testing.T) {
	var m Model
	now := time.Now()
	m.setPinnedNotice("Press Ctrl+C again to detach")
	m.notice = ""
	m.expireNotice(now)
	if m.noticePin {
		t.Error("clearing the notice left the pin armed for the next text")
	}
	m.notice = "fresh"
	m.expireNotice(now)
	m.expireNotice(now.Add(DefaultNoticeTTL + time.Second))
	if m.notice != "" {
		t.Fatalf("notice after a clear inherited stale state: %q", m.notice)
	}
}

func TestConfiguredNoticeTTLIsHonored(t *testing.T) {
	var m Model
	m.noticeTTL = 30 * time.Second
	now := time.Now()
	m.notice = "Saved"
	m.expireNotice(now)
	// Past the 8s default but inside the configured 30s: must survive.
	m.expireNotice(now.Add(DefaultNoticeTTL + time.Second))
	if m.notice == "" {
		t.Fatal("notice expired on the default TTL despite a 30s override")
	}
	m.expireNotice(now.Add(31 * time.Second))
	if m.notice != "" {
		t.Fatalf("notice survived past the configured TTL: %q", m.notice)
	}
}

func TestWithDisplayWiresNoticeTTL(t *testing.T) {
	m := clickModel(nil, t.TempDir())
	m.WithDisplay(config.Display{NoticeTTL: 20})
	if got := m.effectiveNoticeTTL(); got != 20*time.Second {
		t.Errorf("effective TTL = %v, want 20s", got)
	}
	zero := clickModel(nil, t.TempDir())
	zero.WithDisplay(config.Display{})
	if got := zero.effectiveNoticeTTL(); got != DefaultNoticeTTL {
		t.Errorf("unset TTL = %v, want the %v default", got, DefaultNoticeTTL)
	}
}

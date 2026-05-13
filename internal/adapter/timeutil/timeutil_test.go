package timeutil

import (
	"testing"
	"time"
)

var epoch = time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)

func TestSanitise_ZeroReturnsNow(t *testing.T) {
	got := Sanitise(time.Time{}, epoch)
	if !got.Equal(epoch) {
		t.Errorf("zero parsed: got %v, want %v (now)", got, epoch)
	}
}

func TestSanitise_BeyondFutureReturnsNow(t *testing.T) {
	future := epoch.Add(25 * time.Hour) // 25h ahead — beyond 24h limit
	got := Sanitise(future, epoch)
	if !got.Equal(epoch) {
		t.Errorf("far-future parsed: got %v, want %v (now)", got, epoch)
	}
}

func TestSanitise_BeyondPastReturnsNow(t *testing.T) {
	old := epoch.Add(-31 * 24 * time.Hour) // 31 days ago — beyond 30d limit
	got := Sanitise(old, epoch)
	if !got.Equal(epoch) {
		t.Errorf("far-past parsed: got %v, want %v (now)", got, epoch)
	}
}

func TestSanitise_RecentTimeKept(t *testing.T) {
	recent := epoch.Add(-1 * time.Hour) // 1h ago — well within bounds
	got := Sanitise(recent, epoch)
	if !got.Equal(recent) {
		t.Errorf("recent time changed: got %v, want %v", got, recent)
	}
}

func TestSanitise_JustWithinFutureKept(t *testing.T) {
	near := epoch.Add(23 * time.Hour) // 23h ahead — just inside limit
	got := Sanitise(near, epoch)
	if !got.Equal(near) {
		t.Errorf("near-future time changed: got %v, want %v", got, near)
	}
}

func TestSanitise_JustWithinPastKept(t *testing.T) {
	recent := epoch.Add(-29 * 24 * time.Hour) // 29 days ago — inside limit
	got := Sanitise(recent, epoch)
	if !got.Equal(recent) {
		t.Errorf("recent-past time changed: got %v, want %v", got, recent)
	}
}

func TestUnixSeconds_UnixMsStyleValueRejected(t *testing.T) {
	// A unix-milliseconds value (e.g. 1778709402000) is well above 4e9
	// and must be rejected as corrupt.
	unixMs := int64(1_778_709_402_000)
	got := UnixSeconds(unixMs, epoch)
	if !got.Equal(epoch) {
		t.Errorf("unix-ms as unix-s: got %v, want %v (now)", got, epoch)
	}
}

func TestUnixSeconds_ZeroRejected(t *testing.T) {
	got := UnixSeconds(0, epoch)
	if !got.Equal(epoch) {
		t.Errorf("zero unix: got %v, want %v (now)", got, epoch)
	}
}

func TestUnixSeconds_NegativeRejected(t *testing.T) {
	got := UnixSeconds(-1, epoch)
	if !got.Equal(epoch) {
		t.Errorf("negative unix: got %v, want %v (now)", got, epoch)
	}
}

func TestUnixSeconds_ValidValueKept(t *testing.T) {
	// epoch as unix seconds: 1_778_709_600 (approximately)
	raw := epoch.Unix()
	got := UnixSeconds(raw, epoch)
	if !got.Equal(epoch) {
		t.Errorf("valid unix seconds: got %v, want %v", got, epoch)
	}
}

func TestUnixSeconds_FarFutureUnixSecondsRejected(t *testing.T) {
	// 50 years in the future in unix-seconds — within [1e9,4e9] range but
	// rejected by Sanitise's 24h future bound.
	farFuture := epoch.Add(50 * 365 * 24 * time.Hour).Unix()
	got := UnixSeconds(farFuture, epoch)
	if !got.Equal(epoch) {
		t.Errorf("far-future unix-s: got %v, want %v (now)", got, epoch)
	}
}

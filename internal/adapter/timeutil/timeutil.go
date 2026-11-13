// Package timeutil provides shared timestamp helpers for adapter packages.
package timeutil

import "time"

const (
	// maxFuture is the maximum amount of time a payload timestamp may be
	// ahead of the current clock before it is considered corrupt.
	maxFuture = 24 * time.Hour

	// maxPast is the maximum age of a payload timestamp before it is
	// considered stale / corrupt.
	maxPast = 30 * 24 * time.Hour

	// minUnixSeconds is the lower bound for a plausible unix-seconds value
	// (2001-09-09). Values below this are likely unix-milliseconds or bogus.
	minUnixSeconds int64 = 1_000_000_000

	// maxUnixSeconds is the upper bound for a plausible unix-seconds value
	// (2096-10-02). Values above this are likely unix-milliseconds or bogus.
	maxUnixSeconds int64 = 4_000_000_000
)

// Sanitise returns parsed if it falls within an acceptable window around now
// (not more than 24 h in the future, not more than 30 days in the past, and
// not the zero value). Otherwise it returns now, protecting downstream logic
// from corrupt or malicious payload timestamps.
func Sanitise(parsed, now time.Time) time.Time {
	t, _ := SanitiseDated(parsed, now)
	return t
}

// SanitiseDated is Sanitise that also reports whether it kept parsed (true)
// or fell back to now (false). A reading stamped with now has no known age,
// which matters for a retained message (#93).
func SanitiseDated(parsed, now time.Time) (time.Time, bool) {
	if parsed.IsZero() {
		return now, false
	}
	if parsed.After(now.Add(maxFuture)) || parsed.Before(now.Add(-maxPast)) {
		return now, false
	}
	return parsed, true
}

// UnixSeconds converts a raw int64 unix-seconds value to a time.Time and
// sanitises it. If raw is outside the plausible unix-seconds range
// [1_000_000_000, 4_000_000_000] it is treated as corrupt and now is
// returned immediately (before Sanitise is even called), which also catches
// unix-millisecond values accidentally passed as unix-seconds.
func UnixSeconds(raw int64, now time.Time) time.Time {
	t, _ := UnixSecondsDated(raw, now)
	return t
}

// UnixSecondsDated is UnixSeconds that also reports whether the publisher's
// time was kept (true) or replaced by now (false).
func UnixSecondsDated(raw int64, now time.Time) (time.Time, bool) {
	if raw < minUnixSeconds || raw > maxUnixSeconds {
		return now, false
	}
	return SanitiseDated(time.Unix(raw, 0).UTC(), now)
}

// RFC3339Dated parses an RFC3339 publisher timestamp and sanitises it. An
// empty or unparseable value gives now, false.
func RFC3339Dated(raw string, now time.Time) (time.Time, bool) {
	if raw == "" {
		return now, false
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return now, false
	}
	return SanitiseDated(t, now)
}

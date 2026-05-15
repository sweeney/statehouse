package state

import (
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/model"
)

func makeRecord(id, typ string, started time.Time) model.ActivityRecord {
	return model.ActivityRecord{ID: id, Source: "test", Type: typ, StartedAt: started}
}

func TestActivityLog_AppendAndRecent(t *testing.T) {
	l := newActivityLog()
	l.Append(makeRecord("r1", "call", t0))
	l.Append(makeRecord("r2", "call", t0.Add(time.Minute)))

	got := l.Recent(10)
	if len(got) != 2 {
		t.Fatalf("expected 2 records, got %d", len(got))
	}
	// Newest first.
	if got[0].ID != "r2" || got[1].ID != "r1" {
		t.Errorf("expected newest-first order, got %v %v", got[0].ID, got[1].ID)
	}
}

func TestActivityLog_Update(t *testing.T) {
	l := newActivityLog()
	l.Append(makeRecord("r1", "call", t0))

	end := t0.Add(5 * time.Minute)
	l.Update("r1", func(r *model.ActivityRecord) {
		r.EndedAt = &end
	})

	got := l.Recent(1)
	if got[0].EndedAt == nil || !got[0].EndedAt.Equal(end) {
		t.Errorf("expected EndedAt=%v, got %v", end, got[0].EndedAt)
	}
}

func TestActivityLog_UpdateNoopOnMissing(t *testing.T) {
	l := newActivityLog()
	l.Update("ghost", func(r *model.ActivityRecord) { r.Type = "mutated" }) // must not panic
}

func TestActivityLog_UpdatePicksMostRecent(t *testing.T) {
	l := newActivityLog()
	l.Append(makeRecord("dup", "call", t0))
	l.Append(makeRecord("dup", "call", t0.Add(time.Hour)))

	l.Update("dup", func(r *model.ActivityRecord) { r.Type = "door_open" })

	got := l.Recent(2)
	// Most recent "dup" (index 0) should be updated; older one untouched.
	if got[0].Type != "door_open" {
		t.Errorf("expected most-recent dup to be updated, got type=%q", got[0].Type)
	}
	if got[1].Type != "call" {
		t.Errorf("expected older dup to be untouched, got type=%q", got[1].Type)
	}
}

func TestActivityLog_Eviction(t *testing.T) {
	l := newActivityLog()
	for i := 0; i < ActivityLogSize+5; i++ {
		l.Append(makeRecord("r", "call", t0.Add(time.Duration(i)*time.Second)))
	}
	got := l.Recent(ActivityLogSize + 10)
	if len(got) != ActivityLogSize {
		t.Errorf("expected %d entries after overflow, got %d", ActivityLogSize, len(got))
	}
}

func TestActivityLog_LimitRespected(t *testing.T) {
	l := newActivityLog()
	for i := 0; i < 10; i++ {
		l.Append(makeRecord("r", "call", t0.Add(time.Duration(i)*time.Second)))
	}
	got := l.Recent(3)
	if len(got) != 3 {
		t.Errorf("expected 3 with limit=3, got %d", len(got))
	}
}

func TestActivityLog_EmptyReturnsNone(t *testing.T) {
	l := newActivityLog()
	got := l.Recent(10)
	if len(got) != 0 {
		t.Errorf("expected empty slice from empty log, got %d", len(got))
	}
}

package intercom

import (
	"testing"
	"time"

	"github.com/sweeney/statehouse/internal/model"
)

// fakeEngine records all engine calls.
type fakeEngine struct {
	ingested []model.ActivitySignal
	cleared  []string
	recorded []model.ActivityRecord
}

func (f *fakeEngine) IngestSignal(s model.ActivitySignal) { f.ingested = append(f.ingested, s) }
func (f *fakeEngine) ClearSignal(id string, _ time.Time)  { f.cleared = append(f.cleared, id) }
func (f *fakeEngine) RecordActivity(r model.ActivityRecord) {
	f.recorded = append(f.recorded, r)
}
func (f *fakeEngine) UpdateActivity(id string, fn func(*model.ActivityRecord)) {
	for i := range f.recorded {
		if f.recorded[i].ID == id {
			fn(&f.recorded[i])
			return
		}
	}
}

func newTestAdapter() (*Adapter, *fakeEngine) {
	eng := &fakeEngine{}
	return New(eng, "asterisk", nil), eng
}

// --- Payload parsing ---

func TestHandleMessage_RingingCreatesSignal(t *testing.T) {
	a, eng := newTestAdapter()
	payload := `{"event":"ringing","call_id":"call-123","from":{"extension":"11","name":"Office"},"to":{"extension":"12","name":"Games Room"},"timestamp":"2026-05-15T11:01:15Z"}`
	a.HandleMessage("asterisk/call/call-123/ringing", []byte(payload), false)

	if len(eng.ingested) != 1 {
		t.Fatalf("expected 1 IngestSignal call, got %d", len(eng.ingested))
	}
	s := eng.ingested[0]
	if s.ID != "call-123" {
		t.Errorf("expected signal ID 'call-123', got %q", s.ID)
	}
	if s.Type != "call_ringing" {
		t.Errorf("expected type 'call_ringing', got %q", s.Type)
	}
	if s.Source != SchemeName {
		t.Errorf("expected source %q, got %q", SchemeName, s.Source)
	}
	if s.ExpiresAt.IsZero() {
		t.Error("expected safety TTL to be set on ringing signal")
	}
}

func TestHandleMessage_AnsweredUpgradesSignal(t *testing.T) {
	a, eng := newTestAdapter()
	payload := `{"event":"answered","call_id":"call-123","from":{"extension":"11","name":"Office"},"to":{"extension":"12","name":"Games Room"},"timestamp":"2026-05-15T11:01:20Z"}`
	a.HandleMessage("asterisk/call/call-123/answered", []byte(payload), false)

	if len(eng.ingested) != 1 {
		t.Fatalf("expected 1 IngestSignal call, got %d", len(eng.ingested))
	}
	s := eng.ingested[0]
	if s.Type != "call_active" {
		t.Errorf("expected type 'call_active' on answered, got %q", s.Type)
	}
	if s.Confidence < 0.9 {
		t.Errorf("expected higher confidence on answered call, got %.2f", s.Confidence)
	}
}

func TestHandleMessage_HungupClearsSignal(t *testing.T) {
	a, eng := newTestAdapter()
	payload := `{"event":"hungup","call_id":"call-123","from":{"extension":"11","name":"Office"},"to":{"extension":"12","name":"Games Room"},"timestamp":"2026-05-15T11:01:21Z","cause":"cancelled"}`
	a.HandleMessage("asterisk/call/call-123/hungup", []byte(payload), false)

	if len(eng.cleared) != 1 || eng.cleared[0] != "call-123" {
		t.Fatalf("expected ClearSignal('call-123'), got %v", eng.cleared)
	}
	if len(eng.ingested) != 0 {
		t.Errorf("expected no IngestSignal on hungup, got %d", len(eng.ingested))
	}
}

func TestHandleMessage_StatusIsIgnored(t *testing.T) {
	a, eng := newTestAdapter()
	payload := `{"state":"online","timestamp":"2026-05-15T11:00:16Z"}`
	a.HandleMessage("asterisk/status", []byte(payload), false)

	if len(eng.ingested) != 0 || len(eng.cleared) != 0 {
		t.Errorf("status message must not trigger signal calls, ingested=%d cleared=%d", len(eng.ingested), len(eng.cleared))
	}
}

func TestHandleMessage_MalformedPayloadIgnored(t *testing.T) {
	a, eng := newTestAdapter()
	a.HandleMessage("asterisk/call/call-1/ringing", []byte(`not json`), false)
	if len(eng.ingested) != 0 {
		t.Errorf("expected no signal for malformed payload, got %d", len(eng.ingested))
	}
}

func TestHandleMessage_EmptyPayloadIgnored(t *testing.T) {
	a, eng := newTestAdapter()
	a.HandleMessage("asterisk/call/call-1/ringing", []byte{}, false)
	if len(eng.ingested) != 0 {
		t.Errorf("expected no signal for empty payload, got %d", len(eng.ingested))
	}
}

func TestHandleMessage_UnknownEventIgnored(t *testing.T) {
	a, eng := newTestAdapter()
	payload := `{"event":"unknown_event","call_id":"call-1","timestamp":"2026-05-15T11:00:00Z"}`
	a.HandleMessage("asterisk/call/call-1/unknown_event", []byte(payload), false)
	if len(eng.ingested) != 0 || len(eng.cleared) != 0 {
		t.Errorf("unknown event must be ignored, ingested=%d cleared=%d", len(eng.ingested), len(eng.cleared))
	}
}

// --- Call tracking ---

func TestCallTracking_ConcurrentCalls(t *testing.T) {
	a, eng := newTestAdapter()

	ring := func(id string) {
		payload := `{"event":"ringing","call_id":"` + id + `","from":{"extension":"11","name":"Office"},"to":{"extension":"12","name":"Games Room"},"timestamp":"2026-05-15T11:00:00Z"}`
		a.HandleMessage("asterisk/call/"+id+"/ringing", []byte(payload), false)
	}
	hang := func(id string) {
		payload := `{"event":"hungup","call_id":"` + id + `","from":{"extension":"11","name":"Office"},"to":{"extension":"12","name":"Games Room"},"timestamp":"2026-05-15T11:01:00Z","cause":"answered_elsewhere"}`
		a.HandleMessage("asterisk/call/"+id+"/hungup", []byte(payload), false)
	}

	ring("call-1")
	ring("call-2")

	if len(eng.ingested) != 2 {
		t.Fatalf("expected 2 IngestSignal calls, got %d", len(eng.ingested))
	}

	hang("call-1")
	if len(eng.cleared) != 1 || eng.cleared[0] != "call-1" {
		t.Errorf("expected call-1 cleared, got %v", eng.cleared)
	}

	hang("call-2")
	if len(eng.cleared) != 2 || eng.cleared[1] != "call-2" {
		t.Errorf("expected call-2 cleared, got %v", eng.cleared)
	}
}

func TestCallTracking_MetaFields(t *testing.T) {
	a, eng := newTestAdapter()
	payload := `{"event":"ringing","call_id":"call-99","from":{"extension":"11","name":"Office"},"to":{"extension":"12","name":"Games Room"},"timestamp":"2026-05-15T11:00:00Z"}`
	a.HandleMessage("asterisk/call/call-99/ringing", []byte(payload), false)

	s := eng.ingested[0]
	if s.Meta["from_name"] != "Office" {
		t.Errorf("expected from_name='Office' in meta, got %v", s.Meta["from_name"])
	}
	if s.Meta["to_name"] != "Games Room" {
		t.Errorf("expected to_name='Games Room' in meta, got %v", s.Meta["to_name"])
	}
}

// --- Activity record tests ---

func TestActivityRecord_RingingCreatesRecord(t *testing.T) {
	a, eng := newTestAdapter()
	payload := `{"event":"ringing","call_id":"call-1","from":{"extension":"11","name":"Office"},"to":{"extension":"12","name":"Games Room"},"timestamp":"2026-05-15T11:00:00Z"}`
	a.HandleMessage("asterisk/call/call-1/ringing", []byte(payload), false)

	if len(eng.recorded) != 1 {
		t.Fatalf("expected 1 activity record on ringing, got %d", len(eng.recorded))
	}
	r := eng.recorded[0]
	if r.ID != "call-1" {
		t.Errorf("expected record ID 'call-1', got %q", r.ID)
	}
	if r.Source != SchemeName {
		t.Errorf("expected source %q, got %q", SchemeName, r.Source)
	}
	if r.Type != "call" {
		t.Errorf("expected type 'call', got %q", r.Type)
	}
	if r.EndedAt != nil {
		t.Error("expected EndedAt nil on ringing")
	}
	if r.Meta["from_name"] != "Office" {
		t.Errorf("expected from_name='Office', got %v", r.Meta["from_name"])
	}
}

func TestActivityRecord_AnsweredStampsAnsweredAt(t *testing.T) {
	a, eng := newTestAdapter()
	ring := `{"event":"ringing","call_id":"call-1","from":{"extension":"11","name":"Office"},"to":{"extension":"12","name":"Games Room"},"timestamp":"2026-05-15T11:00:00Z"}`
	ans := `{"event":"answered","call_id":"call-1","from":{"extension":"11","name":"Office"},"to":{"extension":"12","name":"Games Room"},"timestamp":"2026-05-15T11:00:05Z"}`
	a.HandleMessage("asterisk/call/call-1/ringing", []byte(ring), false)
	a.HandleMessage("asterisk/call/call-1/answered", []byte(ans), false)

	if len(eng.recorded) != 1 {
		t.Fatalf("expected 1 activity record, got %d", len(eng.recorded))
	}
	r := eng.recorded[0]
	if r.Meta["answered_at"] == nil {
		t.Error("expected answered_at in meta after answered event")
	}
	if r.EndedAt != nil {
		t.Error("expected EndedAt still nil after answered")
	}
}

func TestActivityRecord_HungupSetsEndedAt(t *testing.T) {
	a, eng := newTestAdapter()
	ring := `{"event":"ringing","call_id":"call-1","from":{"extension":"11","name":"Office"},"to":{"extension":"12","name":"Games Room"},"timestamp":"2026-05-15T11:00:00Z"}`
	hung := `{"event":"hungup","call_id":"call-1","from":{"extension":"11","name":"Office"},"to":{"extension":"12","name":"Games Room"},"timestamp":"2026-05-15T11:02:27Z","cause":"normal","talk_duration_seconds":142}`
	a.HandleMessage("asterisk/call/call-1/ringing", []byte(ring), false)
	a.HandleMessage("asterisk/call/call-1/hungup", []byte(hung), false)

	r := eng.recorded[0]
	if r.EndedAt == nil {
		t.Fatal("expected EndedAt set after hungup")
	}
	if r.Meta["cause"] != "normal" {
		t.Errorf("expected cause='normal', got %v", r.Meta["cause"])
	}
	if r.Meta["talk_duration_seconds"] != float64(142) {
		t.Errorf("expected talk_duration_seconds=142, got %v", r.Meta["talk_duration_seconds"])
	}
}

func TestActivityRecord_CancelledHasNoTalkDuration(t *testing.T) {
	a, eng := newTestAdapter()
	ring := `{"event":"ringing","call_id":"call-1","from":{"extension":"11","name":"Office"},"to":{"extension":"12","name":"Games Room"},"timestamp":"2026-05-15T11:00:00Z"}`
	hung := `{"event":"hungup","call_id":"call-1","from":{"extension":"11","name":"Office"},"to":{"extension":"12","name":"Games Room"},"timestamp":"2026-05-15T11:00:04Z","cause":"cancelled","talk_duration_seconds":0}`
	a.HandleMessage("asterisk/call/call-1/ringing", []byte(ring), false)
	a.HandleMessage("asterisk/call/call-1/hungup", []byte(hung), false)

	r := eng.recorded[0]
	if r.EndedAt == nil {
		t.Fatal("expected EndedAt set on cancelled call")
	}
	if _, ok := r.Meta["talk_duration_seconds"]; ok {
		t.Error("expected talk_duration_seconds absent for cancelled call")
	}
}

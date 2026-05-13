package history

import "github.com/sweeney/statehouse/internal/model"

// Sink is an adapter that implements state.EventSink and
// state.CanonicalSink on top of a Log, so derived events and
// canonical events both go through the bounded JSONL file.
type Sink struct {
	Log *Log
}

func (s *Sink) OnDerivedEvent(ev model.DerivedEvent) {
	if s == nil || s.Log == nil {
		return
	}
	_ = s.Log.Append("derived_event", ev)
}

func (s *Sink) OnCanonicalEvent(ev model.CanonicalEvent) {
	if s == nil || s.Log == nil {
		return
	}
	_ = s.Log.Append("canonical_event", ev)
}

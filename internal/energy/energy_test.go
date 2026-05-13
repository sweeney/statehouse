package energy

import (
	"math"
	"testing"
	"time"
)

func TestCounter_DeltaAndReset(t *testing.T) {
	var c Counter
	c.SetBaseline(100.0)
	c.Update(100.4)
	if got := c.Delta(); !approx(got, 0.4) {
		t.Fatalf("delta after small update: %v", got)
	}
	// Plug counter reset: new value below baseline.
	c.Update(0.0)
	c.Update(0.2)
	if got := c.Delta(); !approx(got, 0.2) {
		t.Fatalf("delta after reset and update: %v", got)
	}
}

func TestIntegrator_Trapezoid(t *testing.T) {
	now := time.Date(2026, 5, 13, 9, 0, 0, 0, time.UTC)
	i := NewIntegrator(30 * time.Minute)
	i.Update(now, 0)
	i.Update(now.Add(time.Minute), 2000) // avg = 1000W over 1m
	i.Update(now.Add(2*time.Minute), 2000) // 2000W over 1m
	// 1000W * 1/60 h / 1000 = 0.01666 kWh
	// 2000W * 1/60 h / 1000 = 0.03333 kWh
	want := 1000.0/60/1000 + 2000.0/60/1000
	if !approx(i.Total(), want) {
		t.Fatalf("total = %v want %v", i.Total(), want)
	}
}

func TestIntegrator_GapClamp(t *testing.T) {
	now := time.Date(2026, 5, 13, 1, 0, 0, 0, time.UTC)
	i := NewIntegrator(30 * time.Minute)
	i.Update(now, 60)
	// 4 hour gap -> must not integrate 60W across it.
	i.Update(now.Add(4*time.Hour), 0.3)
	if i.GapsClamped() != 1 {
		t.Fatalf("expected one gap clamped, got %d", i.GapsClamped())
	}
	if i.Total() != 0 {
		t.Fatalf("total should remain 0, got %v", i.Total())
	}
}

func TestIntegrator_HandlesOutOfOrder(t *testing.T) {
	now := time.Date(2026, 5, 13, 1, 0, 0, 0, time.UTC)
	i := NewIntegrator(30 * time.Minute)
	i.Update(now, 1000)
	// Same timestamp / earlier timestamp: must not panic or integrate.
	i.Update(now, 1500)
	i.Update(now.Add(-time.Second), 100)
	if i.Total() != 0 {
		t.Fatalf("total should be zero for non-positive intervals, got %v", i.Total())
	}
}

func TestDivergencePct(t *testing.T) {
	if got := DivergencePct(1.0, 0.28); !approx(got, 72) {
		t.Fatalf("expected ~72%%, got %v", got)
	}
	if got := DivergencePct(0, 0); got != 0 {
		t.Fatalf("zero/zero should be 0, got %v", got)
	}
	if got := DivergencePct(2, 2); got != 0 {
		t.Fatalf("equal -> 0%%, got %v", got)
	}
}

func TestSelectKWh(t *testing.T) {
	v, src := SelectKWh(StrategyCounter, 1.0, 0.3)
	if v != 1.0 || src != "counter" {
		t.Fatalf("counter strategy with both values should pick counter, got %v %s", v, src)
	}
	// Counter is zero (plug never moved): fall back to integration.
	v, src = SelectKWh(StrategyCounter, 0, 0.1)
	if v != 0.1 || src != "integration" {
		t.Fatalf("counter strategy with zero counter should fall back, got %v %s", v, src)
	}
	v, src = SelectKWh(StrategyIntegration, 1.0, 0.3)
	if v != 0.3 || src != "integration" {
		t.Fatalf("integration strategy should pick integration, got %v %s", v, src)
	}
}

func approx(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

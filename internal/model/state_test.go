package model

import (
	"math"
	"testing"
	"time"
)

func TestObserve_IgnoresNonFinite(t *testing.T) {
	t0 := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Second)

	// Non-finite values must never seed an extremum: a NaN seed would pin the
	// value forever (every later compare against NaN is false) and break JSON
	// marshalling of the whole response.
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		var maxE, minE *Extremum
		ObserveMax(&maxE, v, t0)
		ObserveMin(&minE, v, t0)
		if maxE != nil {
			t.Errorf("ObserveMax(%v) must not seed an extremum, got %+v", v, maxE)
		}
		if minE != nil {
			t.Errorf("ObserveMin(%v) must not seed an extremum, got %+v", v, minE)
		}
	}

	// A non-finite value must not poison an already-seeded extremum either.
	maxE := &Extremum{Value: 5, At: t0}
	ObserveMax(&maxE, math.NaN(), t1)
	if maxE.Value != 5 || !maxE.At.Equal(t0) {
		t.Errorf("NaN must not displace a seeded max, got %+v", maxE)
	}
	minE := &Extremum{Value: 5, At: t0}
	ObserveMin(&minE, math.Inf(-1), t1)
	if minE.Value != 5 || !minE.At.Equal(t0) {
		t.Errorf("-Inf must not displace a seeded min, got %+v", minE)
	}
}

func TestObserve_EarliestWinsOnTie(t *testing.T) {
	t0 := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Second)

	var maxE *Extremum
	ObserveMax(&maxE, 10, t0)
	ObserveMax(&maxE, 10, t1) // equal peak, later — must not displace.
	if !maxE.At.Equal(t0) {
		t.Errorf("max tie must retain earliest timestamp %v, got %v", t0, maxE.At)
	}

	var minE *Extremum
	ObserveMin(&minE, 10, t0)
	ObserveMin(&minE, 10, t1)
	if !minE.At.Equal(t0) {
		t.Errorf("min tie must retain earliest timestamp %v, got %v", t0, minE.At)
	}
}

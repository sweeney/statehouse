package device

import "testing"

func TestStalenessSecondsForClass_PowerClasses(t *testing.T) {
	for _, class := range []string{ClassShortBurst, ClassCyclePower, ClassContinuous, ClassMedia} {
		if got := StalenessSecondsForClass(class, nil); got != 900 {
			t.Errorf("class %q: want 900, got %v", class, got)
		}
	}
}

func TestStalenessSecondsForClass_BinaryState(t *testing.T) {
	if got := StalenessSecondsForClass(ClassBinaryState, nil); got != 3600 {
		t.Errorf("want 3600, got %v", got)
	}
}

func TestStalenessSecondsForClass_UnknownClass(t *testing.T) {
	if got := StalenessSecondsForClass("unknown_class", nil); got != 3600 {
		t.Errorf("want 3600 default, got %v", got)
	}
}

func TestStalenessSecondsForClass_OverrideWins(t *testing.T) {
	override := 300
	if got := StalenessSecondsForClass(ClassShortBurst, &override); got != 300 {
		t.Errorf("want 300 (override), got %v", got)
	}
}

func TestStalenessSecondsForClass_NilOverrideUsesDefault(t *testing.T) {
	if got := StalenessSecondsForClass(ClassShortBurst, nil); got != 900 {
		t.Errorf("want 900 (class default), got %v", got)
	}
}

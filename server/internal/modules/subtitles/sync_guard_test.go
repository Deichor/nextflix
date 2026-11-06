package subtitles

import (
	"errors"
	"strings"
	"testing"
)

func TestGuardTiming(t *testing.T) {
	if err := guardTiming(func() error { return nil }); err != nil {
		t.Fatalf("no panic: %v", err)
	}
	want := errors.New("no reference")
	if err := guardTiming(func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("plain error: %v", err)
	}
	var cues []int
	err := guardTiming(func() error {
		_ = cues[3]
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "index out of range") {
		t.Fatalf("panic: %v", err)
	}
}

//go:build !darwin

package official

import (
	"errors"
	"testing"
)

func TestUnsupportedPlatform(t *testing.T) {
	if path, err := Discover(); path != "" || !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Discover = %q, %v", path, err)
	}
	if path, err := Prepare("unused", "unused"); path != "" || !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Prepare = %q, %v", path, err)
	}
	if err := Swap("unused", "unused"); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Swap = %v", err)
	}
}

package hostspec

import (
	"runtime"
	"testing"
)

// TestDetectAnswersOnThisHost. The values themselves are the machine's, so
// there is nothing to pin — what is asserted is the shape a caller relies on:
// Detected and the numbers agree, and nothing that becomes a limit is zero
// while claiming to be known.
func TestDetectAnswersOnThisHost(t *testing.T) {
	spec := Detect(ColimaDir())

	if runtime.GOOS != "darwin" {
		if spec.Detected {
			t.Errorf("Detected on %s, where there is no implementation", runtime.GOOS)
		}
		return
	}

	if !spec.Detected {
		t.Fatal("not detected on darwin; the caps would silently fall back to the absolute limits")
	}
	// A cap is built by subtracting from these two. A zero here would refuse
	// every size while looking like a deliberate answer, which is the failure
	// Detected exists to keep separate from "devhub cannot tell".
	if spec.CPUs <= 0 {
		t.Errorf("CPUs = %d", spec.CPUs)
	}
	if spec.MemoryBytes <= 0 {
		t.Errorf("MemoryBytes = %d", spec.MemoryBytes)
	}
	// Sanity, not a pin: a machine reporting a terabyte of RAM or 4096 cores
	// means the sysctl was misread, and the caps would then permit anything.
	if spec.MemoryBytes > 1<<50 {
		t.Errorf("MemoryBytes = %d, implausible — the sysctl was misread", spec.MemoryBytes)
	}
	if spec.CPUs > 4096 {
		t.Errorf("CPUs = %d, implausible", spec.CPUs)
	}
}

// TestFreeDiskIsOptional: the disk figure is shown, never refused on, so losing
// it must not cost the whole probe. A directory that does not exist is the
// realistic way that happens.
func TestFreeDiskIsOptional(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("no implementation off darwin")
	}
	spec := Detect("/nonexistent/devhub/test/path")
	if !spec.Detected {
		t.Error("an unreadable volume sank the whole probe")
	}
	if spec.FreeDiskBytes != 0 {
		t.Errorf("FreeDiskBytes = %d, want 0 for an unreadable path", spec.FreeDiskBytes)
	}
	if spec.MemoryBytes <= 0 {
		t.Error("memory was lost along with the disk figure")
	}
}

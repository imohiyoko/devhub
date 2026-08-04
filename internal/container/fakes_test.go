package container

// Doubles for the two seams that would otherwise reach a real daemon: an
// Adapter and a ProfileLister. Every test in this package builds its Runtime
// from these, so `go test` never talks to the developer's own Docker or Colima
// — including the containerd path, which would otherwise shell out to Colima.
//
// They are deliberately thin. The tests here cover capability reporting and
// adapter selection, which only ever ask an Adapter whether it is available;
// the operational methods are exercised against a fake command runner in each
// adapter's own test, where the argv is the thing worth asserting on. A double
// that recorded calls nobody inspects would be scaffolding the compiler cannot
// flag, because Go does not report unused struct fields.

import "context"

// fakeCompose answers as an engine adapter without an engine.
type fakeCompose struct {
	// unavailable is what Available reports; nil means the engine is usable.
	unavailable error
}

func (f *fakeCompose) Available(context.Context) error { return f.unavailable }

func (f *fakeCompose) ServiceStates(context.Context, Spec, ComposeSpec) (map[string]State, error) {
	return map[string]State{}, nil
}

func (f *fakeCompose) Up(context.Context, Spec, ComposeSpec) error { return nil }

func (f *fakeCompose) Stop(context.Context, Spec, ComposeSpec) error { return nil }

// fakeColima answers capability probes without Colima — and, on a macOS runner
// that happens to have it, without the real one.
type fakeColima struct {
	profiles []ColimaProfile
	err      error
	// calls counts probes, so a test can assert a non-Colima spec does not pay
	// for one.
	calls int
}

func (f *fakeColima) Profiles(context.Context) ([]ColimaProfile, error) {
	f.calls++
	return f.profiles, f.err
}

type testDeps struct {
	compose    *fakeCompose
	containerd *fakeCompose
	colima     *fakeColima
	inventory  Lister
}

// newTestRuntime builds a Runtime whose every seam is a double. The defaults
// stand in for a host with neither engine present, which is what an
// unconfigured test should see.
func newTestRuntime(d testDeps) *Runtime {
	if d.compose == nil {
		d.compose = &fakeCompose{}
	}
	if d.containerd == nil {
		d.containerd = &fakeCompose{}
	}
	if d.colima == nil {
		d.colima = &fakeColima{err: ErrColimaMissing}
	}
	if d.inventory == nil {
		d.inventory = &fakeLister{}
	}
	// Every field, because Runtime documents that all of them are required and
	// dereferences without a nil check: a double that left one out would fail
	// inside the method rather than at the line that wired it, which is the
	// outcome that invariant exists to avoid.
	return &Runtime{Docker: d.compose, Containerd: d.containerd, Colima: d.colima, Inventory: d.inventory}
}

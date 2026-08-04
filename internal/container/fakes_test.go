package container

// Doubles for the two seams that would otherwise reach a real daemon: an
// Adapter and a ProfileLister. Every test in this package builds its Runtime
// from these, so `go test` never talks to the developer's own Docker or Colima
// — including the containerd path, which would otherwise shell out to Colima.

import "context"

// fakeCompose answers as an engine adapter without an engine.
type fakeCompose struct {
	states map[string]map[string]State // project -> service -> state
	err    error
	// unavailable is what Available reports; nil means the engine is usable.
	unavailable error
	calls       []ComposeSpec
	// runtimes records the spec of every operation, so a test can assert the
	// engine that was addressed.
	runtimes []Spec
	upErr    error
	stopErr  error
}

func (f *fakeCompose) Available(context.Context) error { return f.unavailable }

func (f *fakeCompose) ServiceStates(_ context.Context, rt Spec, spec ComposeSpec) (map[string]State, error) {
	f.calls = append(f.calls, spec)
	f.runtimes = append(f.runtimes, rt)
	if f.err != nil {
		return nil, f.err
	}
	return f.states[spec.Project], nil
}

func (f *fakeCompose) Up(_ context.Context, rt Spec, spec ComposeSpec) error {
	f.calls = append(f.calls, spec)
	f.runtimes = append(f.runtimes, rt)
	return f.upErr
}

func (f *fakeCompose) Stop(_ context.Context, rt Spec, spec ComposeSpec) error {
	f.calls = append(f.calls, spec)
	f.runtimes = append(f.runtimes, rt)
	return f.stopErr
}

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
	return &Runtime{Docker: d.compose, Containerd: d.containerd, Colima: d.colima}
}

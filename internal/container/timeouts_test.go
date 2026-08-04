package container

// The self-bounding invariant, pinned across all three seam implementations.
//
// Adapter and ProfileLister both document that an implementation applies its
// own deadline, because Providers and Warnings apply none: there is no outer
// net any more. Prose alone would not hold that — deleting a WithTimeout from
// an adapter changes no argv and breaks no other assertion, so nothing would
// fail. This does.
//
// It also fixes the docker/nerdctl parity. The two constants live in
// runtime_docker.go and are used from runtime_containerd.go, a cross-file
// coupling with nothing else to show for it; here a `stop` that quietly picked
// up the 10s read budget, or a `ps` that picked up the 5m one, is a failure.

import (
	"context"
	"testing"
	"time"
)

func TestSeamsBoundTheirOwnCalls(t *testing.T) {
	spec := ComposeSpec{Project: "platform-local", Services: []string{"mysql"}}

	for _, tc := range []struct {
		name string
		want time.Duration
		call func(*fakeRunner) error
	}{
		{"docker Available", composeProbeTimeout, func(r *fakeRunner) error {
			return testCompose(r).Available(context.Background())
		}},
		{"docker ServiceStates", composeProbeTimeout, func(r *fakeRunner) error {
			_, err := testCompose(r).ServiceStates(context.Background(), Spec{}, spec)
			return err
		}},
		{"docker Up", composeOpTimeout, func(r *fakeRunner) error {
			return testCompose(r).Up(context.Background(), Spec{}, spec)
		}},
		{"docker Stop", composeOpTimeout, func(r *fakeRunner) error {
			return testCompose(r).Stop(context.Background(), Spec{}, spec)
		}},
		{"nerdctl ServiceStates", composeProbeTimeout, func(r *fakeRunner) error {
			_, err := testNerdctl(r).ServiceStates(context.Background(), containerdRT("dev"), spec)
			return err
		}},
		{"nerdctl Up", composeOpTimeout, func(r *fakeRunner) error {
			return testNerdctl(r).Up(context.Background(), containerdRT("dev"), spec)
		}},
		{"nerdctl Stop", composeOpTimeout, func(r *fakeRunner) error {
			return testNerdctl(r).Stop(context.Background(), containerdRT("dev"), spec)
		}},
		{"colima Profiles", colimaProbeTimeout, func(r *fakeRunner) error {
			_, err := testColima(r, true).Profiles(context.Background())
			return err
		}},
	} {
		// Empty stdout parses cleanly for both `compose ps` and `colima list`,
		// which lets one table cover all three seams.
		runner := &fakeRunner{}
		if err := tc.call(runner); err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if len(runner.calls) == 0 {
			t.Errorf("%s: ran no command", tc.name)
			continue
		}
		call := runner.calls[0]
		if !call.bounded {
			t.Errorf("%s: ran with no deadline; the caller sets none, so this call is unbounded", tc.name)
			continue
		}
		if call.budget != tc.want {
			t.Errorf("%s: budget = %v, want %v", tc.name, call.budget, tc.want)
		}
	}
}

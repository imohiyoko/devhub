package container

// Colima capability detection. Colima is an optional dependency (plan §6.2):
// it is only consulted on macOS with the CLI present, and no colima command
// escapes this file. ColimaProfile does cross the boundary — it is what a
// listing returns — but nothing outside runs colima or decides what its output
// means, and the package and its consumers otherwise speak in Spec, Provider
// and docker contexts.
//
// Everything here is read-only. devhub never starts, stops or reconfigures a
// profile: that is a slow, resource-reserving operation with effects far
// outside devhub, so it stays the user's call (plan §13).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/imohiyoko/devhub/internal/platform"
)

var (
	ErrColimaUnsupportedOS = errors.New("Colima は macOS でのみ利用できます")
	ErrColimaMissing       = errors.New("colima コマンドが見つかりません")
)

// colimaProbeTimeout bounds one `colima list`. Profiles applies it itself, for
// the same reason the compose adapter bounds its own calls: the capability
// report and the switch plan both reach this through an interface, and neither
// should have to know what listing VMs is allowed to cost.
const colimaProbeTimeout = 10 * time.Second

// ColimaProfile is one Colima VM as devhub reads it.
type ColimaProfile struct {
	Name   string
	Status string // colima's own wording: Running / Stopped / Broken …
	// Engine is the container runtime the VM was created with. It is empty
	// when colima does not report one, which is the normal case for a stopped
	// profile — the engine is a property of the running VM, and devhub says
	// "unknown" rather than guessing (plan §6.4).
	Engine string
	Arch   string
}

func (p ColimaProfile) running() bool { return strings.EqualFold(p.Status, "Running") }

// ProfileLister reports the profiles this host offers. Runtime holds it as an
// interface so tests cover the Colima-absent and non-macOS paths on any CI
// runner.
type ProfileLister interface {
	Profiles(ctx context.Context) ([]ColimaProfile, error)
}

// colimaCLI talks to the local Colima via its CLI.
type colimaCLI struct {
	runner   commandRunner
	lookPath func(string) (string, error)
	// darwin gates every call. Colima exists to run a Linux VM on macOS; on
	// Linux and Windows devhub must not shell out to it at all, so this is a
	// field rather than a runtime.GOOS check buried in the call path.
	darwin bool
}

func newColimaCLI() *colimaCLI {
	return &colimaCLI{runner: execRunner{}, lookPath: exec.LookPath, darwin: platform.IsDarwin()}
}

// Profiles lists the Colima profiles on this host. The two "not available"
// cases are distinguished because they need different actions from the user:
// a non-macOS host will never have Colima, while a macOS host without it can
// install it.
func (c *colimaCLI) Profiles(ctx context.Context) ([]ColimaProfile, error) {
	if !c.darwin {
		return nil, ErrColimaUnsupportedOS
	}
	if _, err := c.lookPath("colima"); err != nil {
		return nil, ErrColimaMissing
	}
	// Bounded only from here: the two checks above answer without spawning
	// anything, so a host with no Colima keeps saying so — with its own reason
	// — even when the context it was handed has already expired.
	ctx, cancel := context.WithTimeout(ctx, colimaProbeTimeout)
	defer cancel()
	stdout, stderr, err := c.runner.Run(ctx, "", "colima", "list", "--json")
	if err != nil {
		return nil, cliError(stderr, err)
	}
	return parseColimaList(stdout)
}

// colimaListEntry is the subset of `colima list --json` devhub reads. Colima
// names the engine field "runtime"; it is translated to Engine here so the
// word "runtime" keeps meaning the execution base everywhere else.
type colimaListEntry struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Arch    string `json:"arch"`
	Runtime string `json:"runtime"`
}

// parseColimaList reads `colima list --json`, which prints one JSON object per
// line. A host with no profile at all prints nothing and exits zero (verified
// against colima 0.10.1), which must read as an empty list rather than an
// error: "Colima is installed but you have no VM yet" is a normal state.
func parseColimaList(out string) ([]ColimaProfile, error) {
	var profiles []ColimaProfile
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry colimaListEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, fmt.Errorf("colima list の出力を解釈できません: %w", err)
		}
		if entry.Name == "" {
			continue
		}
		profiles = append(profiles, ColimaProfile{
			Name: entry.Name, Status: entry.Status, Engine: entry.Runtime, Arch: entry.Arch,
		})
	}
	return profiles, nil
}

// colimaDockerContext is the Docker context Colima creates for a profile.
// This mirrors Colima's own profile-name normalisation rather than a plain
// "colima-"+name: the default profile's context is plainly "colima" (and
// "colima" is an accepted spelling of "default"), and a name that already
// carries the prefix is used as-is. Getting this wrong would point `--context`
// at a VM that does not exist.
func colimaDockerContext(profile string) string {
	if profile == "" || profile == DefaultColimaProfile || profile == "colima" {
		return "colima"
	}
	if strings.HasPrefix(profile, "colima-") {
		return profile
	}
	return "colima-" + profile
}

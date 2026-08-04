package container

// The capability surface: what execution bases this host can offer, which
// container engines each one can run, and — for Colima — which profiles exist
// and what state they are in.
//
// Callers render whatever this returns instead of hardcoding provider or
// engine names (plan §6.4), so a host with no Docker and no Colima still gets
// a coherent answer: the providers are listed as unavailable with the reason
// devhub actually observed.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
)

// Profile is one Colima profile as the UI sees it.
type Profile struct {
	Name   string
	Status string
	// Engine is Colima's own value, verbatim. It is empty when it cannot be
	// observed (a stopped profile does not report one); the UI shows that as
	// unknown, and devhub does not infer it.
	Engine string
	Arch   string
	// Context is the Docker context this profile's docker engine listens on.
	// It is what devhub would pass per command, and never something it sets
	// globally (plan §6.3).
	Context string
	// Supported is false when the profile runs an engine devhub has no adapter
	// for — Colima can also host incus, which this tool cannot drive. Reason
	// says which engine that is, so such a profile is listed with an
	// explanation rather than hidden, or worse, offered as a valid choice that
	// save-time validation would then reject.
	Supported bool
	Reason    string
}

// Provider is one execution base and what it can currently do.
type Provider struct {
	ID        string
	Label     string
	Available bool
	// Supported is false when this OS can never offer the provider, as opposed
	// to Available, which is about right now. The distinction is what the UI
	// needs to tell "install Colima and it will work" from "you are on Linux"
	// — the first is worth showing with its reason, the second is noise and is
	// hidden entirely (plan §10).
	Supported bool
	// Reason explains an unavailable provider. It is empty when Available.
	Reason string
	// Engines are the container engines this provider can run. Empty for the
	// host provider, which runs processes rather than containers.
	Engines  []string
	Profiles []Profile
}

// Providers reports the execution bases available on this host. It is
// read-only: it looks for CLIs and lists Colima profiles, and starts nothing.
// Each probe bounds itself, so the report as a whole cannot hang even though
// nothing here sets a deadline.
//
// The two probes run concurrently because they are independent and must not
// spend each other's budget. Run in sequence under a caller's deadline, a
// Docker probe that took all of it handed Colima an already-expired context,
// and a Colima that was installed and running came back unavailable with a
// deadline error — which costs the user their profile list, and that list is
// what the runtime picker is built from. The failure showed up exactly when
// Docker is slowest to answer: right after the daemon or the VM starts, which
// is also when someone is most likely to be opening devhub.
func (r *Runtime) Providers(ctx context.Context) []Provider {
	var (
		wg          sync.WaitGroup
		dockerErr   error
		profiles    []ColimaProfile
		profilesErr error
	)
	wg.Add(2)
	go func() { defer wg.Done(); dockerErr = r.Docker.Available(ctx) }()
	go func() { defer wg.Done(); profiles, profilesErr = r.Colima.Profiles(ctx) }()
	wg.Wait()

	host := Provider{ID: ProviderHost, Label: "ホスト", Available: true, Supported: true, Engines: []string{}}

	docker := Provider{ID: ProviderDocker, Label: "Docker", Available: true, Supported: true, Engines: []string{EngineDocker}}
	if dockerErr != nil {
		docker.Available, docker.Reason = false, dockerErr.Error()
	}

	// Colima advertises the engines devhub can drive on it, not every engine
	// Colima itself can host: an option the user could pick and devhub could
	// not act on is worse than one that is absent with a reason.
	colima := Provider{ID: ProviderColima, Label: "Colima", Supported: true, Engines: drivableEngines()}
	if profilesErr != nil {
		colima.Reason = profilesErr.Error()
		// The one failure that is about the machine rather than its setup: no
		// amount of installing makes Colima available on Linux or Windows.
		colima.Supported = !errors.Is(profilesErr, ErrColimaUnsupportedOS)
	} else {
		colima.Available = true
		for _, p := range profiles {
			supported, reason := engineSupport(p.Engine)
			colima.Profiles = append(colima.Profiles, Profile{
				Name: p.Name, Status: p.Status, Engine: p.Engine, Arch: p.Arch,
				Context: colimaDockerContext(p.Name), Supported: supported, Reason: reason,
			})
		}
	}

	return []Provider{host, docker, colima}
}

// ComposeFor picks the adapter for an environment's declared engine. The
// choice follows the *declaration*, not what the profile turns out to be
// running: devhub never silently switches engines (plan §6.4), so a definition
// that disagrees with reality is driven as written and reported by
// Warnings rather than quietly re-routed.
func (r *Runtime) ComposeFor(rt Spec) (Adapter, error) {
	if rt.Engine != EngineContainerd {
		return r.Docker, nil
	}
	if rt.Provider != ProviderColima {
		return nil, errContainerdUnsupported
	}
	return r.Containerd, nil
}

// DockerContextFor is the Docker context an environment's compose commands
// must run in: a Colima profile's own context, or "" — the ambient context —
// for the plain docker provider. Returning "" rather than the resolved name of
// the current context matters: devhub passes no --context at all there, so a
// user who switches contexts in their shell gets what they expect, and devhub
// still never runs `docker context use` (plan §6.3).
func DockerContextFor(rt Spec) string {
	if rt.Provider == ProviderColima {
		return colimaDockerContext(rt.Profile)
	}
	return ""
}

// Warnings reports what the user should know before devhub drives an
// environment's containers: the declared profile is missing, is not running,
// or runs a different engine than the definition claims. They are warnings
// rather than errors because devhub does not repair any of it — starting or
// reconfiguring a profile is the user's call (plan §6.4, §13) — and because a
// switch may not touch a container component at all.
//
// Only a colima spec pays the probe; anything else returns immediately. The
// probe that does run bounds itself, so a plan can never hang on it.
func (r *Runtime) Warnings(ctx context.Context, rt Spec) []string {
	if rt.Provider != ProviderColima {
		return nil
	}
	profiles, err := r.Colima.Profiles(ctx)
	if err != nil {
		return []string{fmt.Sprintf("Colima の状態を確認できません: %v。コンテナの操作は失敗する可能性があります。", err)}
	}

	name := colimaProfileFor(rt)
	i := slices.IndexFunc(profiles, func(p ColimaProfile) bool { return p.Name == name })
	if i < 0 {
		return []string{fmt.Sprintf("Colima profile '%s' が見つかりません。`colima start -p %s` で作成してください。", name, name)}
	}

	var warnings []string
	profile := profiles[i]
	if !profile.running() {
		warnings = append(warnings,
			fmt.Sprintf("Colima profile '%s' は %s です。devhub は profile を起動しないので、`colima start -p %s` を実行してください。", name, profile.Status, name))
	}
	// An engine devhub cannot observe is not a mismatch: a stopped profile
	// reports none, and the warning above already covers that case.
	if profile.Engine != "" && rt.Engine != "" && profile.Engine != rt.Engine {
		warnings = append(warnings, fmt.Sprintf(
			"設定は engine '%s' ですが profile '%s' は '%s' で動いています。devhub は engine を切り替えません（既存のイメージとコンテナに影響するため）。別 profile を作るか、profile を作り直してください。",
			rt.Engine, name, profile.Engine))
	}
	if supported, reason := engineSupport(profile.Engine); !supported {
		warnings = append(warnings, fmt.Sprintf("profile '%s': %s", name, reason))
	}
	// The readiness gap is a property of the engine, not of this profile's
	// health, so it is reported whenever containerd will actually be used.
	// Saying it once up front beats a dependent component failing to connect
	// and looking like a flaky start.
	if rt.Engine == EngineContainerd {
		warnings = append(warnings,
			"containerd では起動完了（healthcheck）を待てません（`nerdctl compose up` に --wait がないため）。依存する component が先に起動する可能性があります。")
	}
	return warnings
}

// drivableEngines are the container engines devhub has an adapter for. It is
// what a provider advertises and what decides whether a profile is usable, so
// devhub never offers an engine it cannot actually drive.
func drivableEngines() []string { return []string{EngineDocker, EngineContainerd} }

// engineSupport judges whether devhub can drive a profile. Colima also hosts
// engines this tool has no adapter for (incus, and containerd until its
// adapter lands), and such a profile must be reported as unusable up front
// rather than offered and then rejected later. An engine devhub cannot
// observe — a stopped profile reports none — is not called unsupported:
// nothing is known about it yet.
func engineSupport(engine string) (bool, string) {
	if engine == "" || slices.Contains(drivableEngines(), engine) {
		return true, ""
	}
	return false, fmt.Sprintf("engine '%s' に対応するアダプタがありません", engine)
}

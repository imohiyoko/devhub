package envs

// The runtime capability surface (plan §9 `GET /api/envs/runtimes`): what
// execution bases this host can offer, which container engines each one can
// run, and — for Colima — which profiles exist and what state they are in.
//
// The UI renders whatever this returns instead of hardcoding provider or
// engine names (plan §6.4), so a host with no Docker and no Colima still gets
// a coherent answer: the providers are listed as unavailable with the reason
// devhub actually observed.

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// runtimeProbeTimeout bounds the whole capability report. The runtimes
// endpoint is on the UI's load path, so it must fail rather than hang.
const runtimeProbeTimeout = 10 * time.Second

// RuntimeProfile is one Colima profile as the UI sees it.
type RuntimeProfile struct {
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

// RuntimeProvider is one execution base and what it can currently do.
type RuntimeProvider struct {
	ID        string
	Label     string
	Available bool
	// Reason explains an unavailable provider. It is empty when Available.
	Reason string
	// Engines are the container engines this provider can run. Empty for the
	// host provider, which runs processes rather than containers.
	Engines  []string
	Profiles []RuntimeProfile
}

// RuntimeProviders reports the execution bases available on this host. It is
// read-only: it looks for CLIs and lists Colima profiles, and starts nothing.
func (c *Controller) RuntimeProviders(ctx context.Context) []RuntimeProvider {
	host := RuntimeProvider{ID: providerHost, Label: "ホスト", Available: true, Engines: []string{}}

	docker := RuntimeProvider{ID: providerDocker, Label: "Docker", Available: true, Engines: []string{engineDocker}}
	if err := c.compose.Available(ctx); err != nil {
		docker.Available, docker.Reason = false, err.Error()
	}

	// Colima advertises the engines devhub can drive on it, not every engine
	// Colima itself can host: an option the user could pick and devhub could
	// not act on is worse than one that is absent with a reason.
	colima := RuntimeProvider{ID: providerColima, Label: "Colima", Engines: drivableEngines()}
	profiles, err := c.colima.Profiles(ctx)
	if err != nil {
		colima.Reason = err.Error()
	} else {
		colima.Available = true
		for _, p := range profiles {
			supported, reason := engineSupport(p.Engine)
			colima.Profiles = append(colima.Profiles, RuntimeProfile{
				Name: p.Name, Status: p.Status, Engine: p.Engine, Arch: p.Arch,
				Context: colimaDockerContext(p.Name), Supported: supported, Reason: reason,
			})
		}
	}

	return []RuntimeProvider{host, docker, colima}
}

// composeFor picks the adapter for an environment's declared engine. The
// choice follows the *declaration*, not what the profile turns out to be
// running: devhub never silently switches engines (plan §6.4), so a definition
// that disagrees with reality is driven as written and reported by
// RuntimeWarnings rather than quietly re-routed.
func (c *Controller) composeFor(rt runtimeSpec) (composeAdapter, error) {
	if rt.Engine != engineContainerd {
		return c.compose, nil
	}
	if rt.Provider != providerColima {
		return nil, errContainerdUnsupported
	}
	return c.containerd, nil
}

// dockerContextFor is the Docker context an environment's compose commands
// must run in: a Colima profile's own context, or "" — the ambient context —
// for the plain docker provider. Returning "" rather than the resolved name of
// the current context matters: devhub passes no --context at all there, so a
// user who switches contexts in their shell gets what they expect, and devhub
// still never runs `docker context use` (plan §6.3).
func dockerContextFor(rt runtimeSpec) string {
	if rt.Provider == providerColima {
		return colimaDockerContext(rt.Profile)
	}
	return ""
}

// RuntimeWarnings reports what the user should know before devhub drives an
// environment's containers: the declared profile is missing, is not running,
// or runs a different engine than the definition claims. They are warnings
// rather than errors because devhub does not repair any of it — starting or
// reconfiguring a profile is the user's call (plan §6.4, §13) — and because a
// switch may not touch a container component at all.
//
// Only a colima environment pays the probe; anything else returns immediately.
func (c *Controller) RuntimeWarnings(ctx context.Context, env environment) []string {
	rt := env.Runtime
	if rt.Provider != providerColima {
		return nil
	}
	profiles, err := c.colima.Profiles(ctx)
	if err != nil {
		return []string{fmt.Sprintf("Colima の状態を確認できません: %v。コンテナの操作は失敗する可能性があります。", err)}
	}

	name := colimaProfileFor(rt)
	i := slices.IndexFunc(profiles, func(p colimaProfile) bool { return p.Name == name })
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
	if rt.Engine == engineContainerd {
		warnings = append(warnings,
			"containerd では起動完了（healthcheck）を待てません（`nerdctl compose up` に --wait がないため）。依存する component が先に起動する可能性があります。")
	}
	return warnings
}

// drivableEngines are the container engines devhub has an adapter for. It is
// what a provider advertises and what decides whether a profile is usable, so
// devhub never offers an engine it cannot actually drive.
func drivableEngines() []string { return []string{engineDocker, engineContainerd} }

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

// runtimeProvidersJSON renders the capability report for the API. Slices are
// materialised as empty arrays rather than null so the UI can iterate without
// null checks.
func runtimeProvidersJSON(providers []RuntimeProvider) map[string]any {
	out := make([]any, 0, len(providers))
	for _, p := range providers {
		profiles := make([]any, 0, len(p.Profiles))
		for _, pr := range p.Profiles {
			profiles = append(profiles, map[string]any{
				"name": pr.Name, "status": pr.Status, "engine": pr.Engine,
				"arch": pr.Arch, "context": pr.Context,
				"supported": pr.Supported, "reason": pr.Reason,
			})
		}
		engines := make([]any, 0, len(p.Engines))
		for _, e := range p.Engines {
			engines = append(engines, e)
		}
		out = append(out, map[string]any{
			"id": p.ID, "label": p.Label, "available": p.Available, "reason": p.Reason,
			"engines": engines, "profiles": profiles,
		})
	}
	return map[string]any{"providers": out}
}

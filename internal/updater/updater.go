// Package updater checks GitHub Releases for a newer devhub and, for the
// installer edition, downloads and swaps in the new binary. It deliberately
// does NOT auto-update: the server only ever checks (a cached, read-only call
// to the GitHub API) and performs an update when the user explicitly asks.
//
// The download path mirrors install.sh / install.ps1 exactly — same asset
// names, same checksums.txt (SHA256) pinning, same optional cosign keyless
// verification — so a self-update is byte-for-byte what a fresh install would
// place, verified the same way.
package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/imohiyoko/devhub/internal/platform"
)

// defaultRepo is the release source. Overridable with DEVHUB_REPO for parity
// with the install scripts (e.g. a fork or a mirror).
const defaultRepo = "imohiyoko/devhub"

func repo() string {
	if r := strings.TrimSpace(os.Getenv("DEVHUB_REPO")); r != "" {
		return r
	}
	return defaultRepo
}

// apiClient talks to the GitHub JSON API (small, fast); dlClient downloads
// release assets (larger, so a longer ceiling). Both are also bounded by the
// caller's context deadline.
var (
	apiClient = &http.Client{Timeout: 15 * time.Second}
	dlClient  = &http.Client{Timeout: 3 * time.Minute}
)

// Release is the subset of a GitHub release the updater needs.
type Release struct {
	Tag string `json:"tag_name"`
}

// Status is the payload of GET /api/update/status.
type Status struct {
	Current         string `json:"current"`                // running version (main.version)
	Latest          string `json:"latest,omitempty"`       // latest stable release tag
	UpdateAvailable bool   `json:"update_available"`       // Latest is strictly newer than Current
	Edition         string `json:"edition"`                // code / homebrew / installer
	CanSelfUpdate   bool   `json:"can_self_update"`        // installer edition + an update exists
	UpgradeHint     string `json:"upgrade_hint,omitempty"` // manual command for non-installer editions
	Disabled        bool   `json:"disabled,omitempty"`     // update check turned off in settings
	Error           string `json:"error,omitempty"`        // check failed (offline etc.); surfaced silently
}

// latest-release cache. A page load hits Status(); without caching every open
// tab would spend a GitHub API call (60/hr unauthenticated, per IP). Only
// successes are cached — an error leaves the cache untouched so the next call
// retries rather than being stuck offline for the whole TTL.
var (
	cacheMu   sync.Mutex
	cachedRel Release
	cachedAt  time.Time
)

const cacheTTL = 6 * time.Hour

// Latest returns the newest stable release (GitHub's releases/latest already
// excludes drafts and prereleases), cached for cacheTTL.
func Latest(ctx context.Context) (Release, error) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if !cachedAt.IsZero() && time.Since(cachedAt) < cacheTTL {
		return cachedRel, nil
	}
	rel, err := fetchLatest(ctx)
	if err != nil {
		return Release{}, err
	}
	cachedRel, cachedAt = rel, time.Now()
	return rel, nil
}

func fetchLatest(ctx context.Context) (Release, error) {
	url := "https://api.github.com/repos/" + repo() + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("User-Agent", "devhub-updater")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := apiClient.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("github api: %s", resp.Status)
	}
	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return Release{}, err
	}
	if rel.Tag == "" {
		return Release{}, fmt.Errorf("github api: empty tag_name")
	}
	return rel, nil
}

// Check reports whether a newer version exists and how the user can move to
// it. For a source/dev run it returns immediately WITHOUT any network call:
// there is nothing to update, and a from-source setup (the binary-restricted
// environments dev.ps1 targets) should not phone home.
func Check(ctx context.Context, current, edition string) Status {
	st := Status{Current: current, Edition: edition}
	if edition == platform.EditionCode || current == "" || current == "dev" {
		return st
	}
	can, hint := upgradeHint(edition)
	st.UpgradeHint = hint
	rel, err := Latest(ctx)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Latest = normalize(rel.Tag)
	st.UpdateAvailable = IsNewer(rel.Tag, current)
	st.CanSelfUpdate = can && st.UpdateAvailable
	return st
}

// upgradeHint returns, per edition, whether one-click self-update applies and —
// when it doesn't — the manual command to run instead. Homebrew owns its own
// binary under the Cellar/Caskroom, so overwriting it ourselves would fight the
// package manager; point the user at brew instead.
func upgradeHint(edition string) (canSelf bool, hint string) {
	switch edition {
	case platform.EditionInstaller:
		return true, ""
	case platform.EditionHomebrew:
		return false, "brew upgrade --cask devhub"
	default:
		return false, ""
	}
}

// assetName is the release asset for the current OS/arch. It MUST match
// GoReleaser's name_template (devhub_{Version}_{Os}_{Arch}) and the windows zip
// override, since the install scripts and this updater both depend on it.
func assetName(version string) string {
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("devhub_%s_%s_%s.%s", normalize(version), runtime.GOOS, runtime.GOARCH, ext)
}

// binName is the executable's name inside the archive (GoReleaser appends .exe
// on windows).
func binName() string {
	if runtime.GOOS == "windows" {
		return "devhub.exe"
	}
	return "devhub"
}

// IsNewer reports whether latestTag is a strictly newer version than currentVer.
// Both may carry a leading "v".
func IsNewer(latestTag, currentVer string) bool {
	return compareVer(normalize(latestTag), normalize(currentVer)) > 0
}

func normalize(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }

// compareVer compares two "X.Y.Z[-prerelease]" versions: 1 if a>b, -1 if a<b,
// 0 if equal. A release with no prerelease outranks the same core with one
// (SemVer §11: 1.0.0 > 1.0.0-rc1).
func compareVer(a, b string) int {
	aCore, aPre := splitPre(a)
	bCore, bPre := splitPre(b)
	if c := compareCore(aCore, bCore); c != 0 {
		return c
	}
	switch {
	case aPre == "" && bPre == "":
		return 0
	case aPre == "":
		return 1
	case bPre == "":
		return -1
	default:
		return strings.Compare(aPre, bPre)
	}
}

func splitPre(v string) (core, pre string) {
	if i := strings.IndexByte(v, '-'); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

func compareCore(a, b string) int {
	ap, bp := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		an, bn := atoiSafe(ap, i), atoiSafe(bp, i)
		if an != bn {
			if an > bn {
				return 1
			}
			return -1
		}
	}
	return 0
}

// atoiSafe returns parts[i] as an int, or 0 if missing/non-numeric (so an
// unparseable component sorts as 0 rather than panicking).
func atoiSafe(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	n, err := strconv.Atoi(parts[i])
	if err != nil {
		return 0
	}
	return n
}

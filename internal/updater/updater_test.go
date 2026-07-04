package updater

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v1.2.4", "1.2.3", true},
		{"v1.2.3", "v1.2.3", false},
		{"1.2.3", "1.2.4", false},
		{"v1.3.0", "v1.2.9", true},
		{"v2.0.0", "v1.99.99", true},
		{"v1.0.0", "v1.0.0-rc1", true},  // release outranks its prerelease
		{"v1.0.0-rc1", "v1.0.0", false}, // prerelease is older than release
		{"v1.0.0-rc2", "v1.0.0-rc1", true},
		{"v1.2.10", "v1.2.9", true}, // numeric, not lexical, compare
	}
	for _, c := range cases {
		if got := IsNewer(c.latest, c.current); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestAssetNameStripsLeadingV(t *testing.T) {
	// The version component must never carry a leading "v" (the goreleaser
	// name_template uses the bare {{.Version}}), regardless of how it arrives.
	withV := assetName("v1.2.3")
	withoutV := assetName("1.2.3")
	if withV != withoutV {
		t.Errorf("assetName should ignore leading v: %q vs %q", withV, withoutV)
	}
	if want := "devhub_1.2.3_"; len(withV) < len(want) || withV[:len(want)] != want {
		t.Errorf("assetName(%q) = %q, want prefix %q", "v1.2.3", withV, want)
	}
}

func TestCosignMajor(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want int
	}{
		// Real `cosign version` output: an ASCII-art banner followed by the
		// GitVersion line (cosign v2 installs report v2.x).
		{"v2 with banner", "  ______   ______ ...banner...\ncosign: A tool for Container Signing.\n\nGitVersion:    v2.5.2\nGitCommit:     abc\n", 2},
		{"v3", "GitVersion:    v3.0.1\n", 3},
		{"no v prefix", "GitVersion: 3.1.0\n", 3},
		{"garbage", "command not found", 0},
		{"empty", "", 0},
	}
	for _, c := range cases {
		if got := cosignMajor([]byte(c.out)); got != c.want {
			t.Errorf("%s: cosignMajor = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestChecksumFor(t *testing.T) {
	dir := t.TempDir()
	sums := filepath.Join(dir, "checksums.txt")
	content := "" +
		"aaaa1111  devhub_1.2.3_linux_amd64.tar.gz\n" +
		"bbbb2222  devhub_1.2.3_windows_amd64.zip\n"
	if err := os.WriteFile(sums, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := checksumFor(sums, "devhub_1.2.3_windows_amd64.zip")
	if err != nil {
		t.Fatalf("checksumFor: %v", err)
	}
	if got != "bbbb2222" {
		t.Errorf("checksumFor = %q, want %q", got, "bbbb2222")
	}

	if _, err := checksumFor(sums, "devhub_9.9.9_linux_arm64.tar.gz"); err == nil {
		t.Error("checksumFor for a missing asset should error")
	}
}

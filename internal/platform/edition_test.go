package platform

import "testing"

func TestEdition_EnvOverrideWins(t *testing.T) {
	t.Setenv("DEVHUB_EDITION", "custom")
	if got := Edition("1.2.3"); got != "custom" {
		t.Errorf("Edition with override = %q, want %q", got, "custom")
	}
}

func TestEdition_UnstampedIsCode(t *testing.T) {
	t.Setenv("DEVHUB_EDITION", "")
	for _, v := range []string{"", "dev"} {
		if got := Edition(v); got != EditionCode {
			t.Errorf("Edition(%q) = %q, want %q", v, got, EditionCode)
		}
	}
}

func TestEdition_StampedDefaultsToInstaller(t *testing.T) {
	t.Setenv("DEVHUB_EDITION", "")
	// The test binary doesn't live under a Homebrew path, so a stamped version
	// (anything other than "dev") falls through to the installer edition.
	if got := Edition("1.2.3"); got != EditionInstaller {
		t.Errorf("Edition(1.2.3) = %q, want %q", got, EditionInstaller)
	}
}

func TestIsHomebrewPath(t *testing.T) {
	cases := map[string]bool{
		"/opt/homebrew/Caskroom/devhub/1.2.3/devhub": true,
		"/usr/local/Cellar/devhub/1.2.3/bin/devhub":  true,
		"/opt/homebrew/bin/devhub":                   true,
		"/home/linuxbrew/.linuxbrew/bin/devhub":      true,
		`C:\Users\me\CaskRoom\devhub\devhub.exe`:     true, // case-insensitive + backslashes
		"/usr/local/bin/devhub":                      false,
		"/home/me/.local/bin/devhub":                 false,
		"":                                           false,
	}
	for path, want := range cases {
		if got := IsHomebrewPath(path); got != want {
			t.Errorf("IsHomebrewPath(%q) = %v, want %v", path, got, want)
		}
	}
}

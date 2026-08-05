package docs

import (
	"strings"
	"testing"
	"testing/fstest"

	devhub "github.com/imohiyoko/devhub"
)

func load(t *testing.T, files fstest.MapFS) *Set {
	t.Helper()
	s, err := Load(files)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return s
}

func TestLoadReadsNameDescriptionAndBody(t *testing.T) {
	s := load(t, fstest.MapFS{
		"docs/agent/cli.md":   {Data: []byte("---\ndescription: How agents drive the CLI.\n---\n# CLI\n\nbody text\n")},
		"docs/root/0001-x.md": {Data: []byte("# No front matter\n")},
		"docs/notes.txt":      {Data: []byte("ignored")},
	})

	got := s.List()
	if len(got) != 2 {
		t.Fatalf("List returned %d docs, want 2 (non-.md must be skipped): %+v", len(got), got)
	}
	// Sorted by name, so agent/cli comes first.
	if got[0].Name != "agent/cli" {
		t.Errorf("name = %q, want agent/cli", got[0].Name)
	}
	if got[0].Description != "How agents drive the CLI." {
		t.Errorf("description = %q", got[0].Description)
	}
	if got[1].Description != "" {
		t.Errorf("a doc without front matter should have an empty description, got %q", got[1].Description)
	}

	body, err := s.Show("agent/cli")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if strings.Contains(body, "description:") {
		t.Errorf("front matter leaked into the body: %q", body)
	}
	if !strings.Contains(body, "body text") {
		t.Errorf("body missing: %q", body)
	}
}

func TestShowAcceptsSloppyNames(t *testing.T) {
	s := load(t, fstest.MapFS{
		"docs/agent/cli.md": {Data: []byte("# CLI\n")},
	})
	for _, name := range []string{"agent/cli", "agent/cli.md", "/agent/cli"} {
		if _, err := s.Show(name); err != nil {
			t.Errorf("Show(%q) = %v, want success", name, err)
		}
	}
}

// An unknown name has to be self-correcting: the error is often all the caller
// gets before it decides to give up.
func TestShowUnknownNameSuggests(t *testing.T) {
	s := load(t, fstest.MapFS{
		"docs/agent/troubleshooting.md": {Data: []byte("# T\n")},
		"docs/agent/ai-api.md":          {Data: []byte("# A\n")},
	})

	_, err := s.Show("agent/troubleshoot")
	if err == nil {
		t.Fatal("want an error for an unknown name")
	}
	if !strings.Contains(err.Error(), "agent/troubleshooting") {
		t.Errorf("error should suggest the near miss, got %q", err)
	}

	_, err = s.Show("totally/unrelated")
	if err == nil || !strings.Contains(err.Error(), "devhub docs list") {
		t.Errorf("with no near miss the error should point at the list command, got %v", err)
	}
}

func TestSplitFrontMatterEdgeCases(t *testing.T) {
	for _, tc := range []struct {
		name, src, wantDesc, wantBodyHas string
	}{
		{"quoted", "---\ndescription: \"quoted value\"\n---\nbody", "quoted value", "body"},
		// The pair has to wrap the value, not merely bookend it. Trimming the
		// quote characters from both ends independently ate these.
		{"apostrophe inside", "---\ndescription: use 'foo'\n---\nbody", "use 'foo'", "body"},
		{"mismatched quotes", "---\ndescription: \"a'\n---\nbody", "\"a'", "body"},
		// And only one pair comes off, so a quoted phrase keeps its own quotes.
		{"doubly quoted", "---\ndescription: \"\"inner\"\"\n---\nbody", "\"inner\"", "body"},
		{"folded onto next line", "---\ndescription: first\n  second\n---\nbody", "first second", "body"},
		// The key line carries no value. Joining an empty first piece would put
		// a space in front of the description — invisible in the docs list, and
		// enough to make an exact-match comparison fail for no stated reason.
		{"value entirely on the next line", "---\ndescription:\n  folded value\n---\nbody", "folded value", "body"},
		// And the quotes still come off after the join, not before it.
		{"folded and quoted", "---\ndescription:\n  \"folded value\"\n---\nbody", "folded value", "body"},
		{"stops at next key", "---\ndescription: only this\nstatus: Accepted\n---\nbody", "only this", "body"},
		{"other keys first", "---\nstatus: Accepted\ndescription: after\n---\nbody", "after", "body"},
		{"unterminated fence keeps body", "---\ndescription: x\nbody with no close", "", "body with no close"},
		{"no front matter", "# Title\n", "", "# Title"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			desc, body := splitFrontMatter(tc.src)
			if desc != tc.wantDesc {
				t.Errorf("description = %q, want %q", desc, tc.wantDesc)
			}
			if !strings.Contains(body, tc.wantBodyHas) {
				t.Errorf("body = %q, want it to contain %q", body, tc.wantBodyHas)
			}
		})
	}
}

// The real embedded tree must load, and every doc must carry a description —
// otherwise `docs list` shows a name with nothing to choose by, which defeats
// the point of listing. This is the guard that a newly added doc gets one.
func TestEmbeddedDocsAllHaveDescriptions(t *testing.T) {
	s, err := Load(devhub.Docs)
	if err != nil {
		t.Fatalf("Load(devhub.Docs): %v", err)
	}
	if len(s.List()) == 0 {
		t.Fatal("embedded docs are empty — is docs/ in the go:embed directive?")
	}
	for _, d := range s.List() {
		if d.Description == "" {
			t.Errorf("docs/%s.md has no front-matter description", d.Name)
		}
	}
}

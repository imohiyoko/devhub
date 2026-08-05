// Package docs reads devhub's own documentation out of the embedded docs/ tree
// so both the CLI (`devhub docs list` / `devhub docs show`) and the HTTP surface
// (GET /api/docs) answer from one implementation.
//
// It exists because an agent that hits an error has to be able to find its way
// out without a network fetch or a checkout: the error's hint names a doc, and
// this is what turns that name into text. That only works if the docs travel
// with the binary, which is why the FS is embedded rather than read from disk.
package docs

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Doc is one document as listed. Body is not included — listing every doc's
// full text would be most of the tree, and the point of the list is to let a
// caller choose which single doc to read.
type Doc struct {
	// Name addresses the doc in Show: the path under docs/ without the .md
	// suffix, e.g. "agent/troubleshooting" or "root/0003-devhub-start-explicit".
	Name string `json:"name"`
	// Description is the doc's front-matter description, written for whoever is
	// choosing from the list. Empty when the file has no front matter.
	Description string `json:"description"`
}

// Set is a loaded documentation tree.
type Set struct {
	fsys fs.FS
	docs []Doc
	body map[string]string
}

// Load reads every .md under docs/ in fsys (the embedded devhub.Docs). It walks
// once at startup rather than per request: the tree is small, fixed at build
// time, and this way a malformed file surfaces immediately instead of on the
// first agent that asks for it.
func Load(fsys fs.FS) (*Set, error) {
	s := &Set{fsys: fsys, body: map[string]string{}}
	err := fs.WalkDir(fsys, "docs", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		name := strings.TrimSuffix(strings.TrimPrefix(p, "docs/"), ".md")
		desc, body := splitFrontMatter(string(b))
		s.docs = append(s.docs, Doc{Name: name, Description: desc})
		s.body[name] = body
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(s.docs, func(i, j int) bool { return s.docs[i].Name < s.docs[j].Name })
	return s, nil
}

// List returns every doc, ordered by name.
func (s *Set) List() []Doc { return s.docs }

// Show returns a doc's body (front matter stripped). An unknown name is an
// error that names the closest candidates, so a caller that guessed at a name
// can correct itself from the error alone instead of re-listing.
func (s *Set) Show(name string) (string, error) {
	name = strings.TrimSuffix(strings.Trim(name, "/"), ".md")
	if body, ok := s.body[name]; ok {
		return body, nil
	}
	if near := s.nearby(name); len(near) > 0 {
		return "", fmt.Errorf("no such doc %q; did you mean: %s", name, strings.Join(near, ", "))
	}
	return "", fmt.Errorf("no such doc %q; run `devhub docs list` to see the available names", name)
}

// nearby returns up to 5 names that share a path segment or a substring with
// the given name.
func (s *Set) nearby(name string) []string {
	var out []string
	base, dir := path.Base(name), path.Dir(name)
	for _, d := range s.docs {
		if strings.Contains(d.Name, base) || path.Dir(d.Name) == dir || path.Base(d.Name) == base {
			out = append(out, d.Name)
			if len(out) == 5 {
				break
			}
		}
	}
	return out
}

// splitFrontMatter separates a leading YAML front-matter block from the body and
// returns the block's description value. Only `description:` is read: it is the
// one field the list needs, and a hand-rolled reader for it keeps the module
// free of a YAML dependency.
//
// A description may be quoted and may continue onto following indented lines
// (the common way to write a long one); anything else in the block is ignored.
func splitFrontMatter(src string) (description, body string) {
	rest, ok := strings.CutPrefix(src, "---\n")
	if !ok {
		return "", src
	}
	block, body, ok := strings.Cut(rest, "\n---")
	if !ok {
		// An opening fence with no closing one: treat the whole file as body so a
		// malformed doc is still readable rather than silently emptied.
		return "", src
	}
	// Drop the closing fence's own newline plus the blank line conventionally
	// left after it, so a shown doc starts at its title.
	body = strings.TrimLeft(body, "\n")

	var parts []string
	for _, line := range strings.Split(block, "\n") {
		if v, ok := strings.CutPrefix(line, "description:"); ok {
			parts = append(parts, strings.TrimSpace(v))
			continue
		}
		// Continuation lines of a folded description are indented; a new key is
		// not. Stop at the first new key once we have started collecting.
		if len(parts) > 0 && strings.HasPrefix(line, " ") {
			parts = append(parts, strings.TrimSpace(line))
			continue
		}
		if len(parts) > 0 {
			break
		}
	}
	return strings.Trim(strings.Join(parts, " "), ` "'`), body
}

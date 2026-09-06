package skill

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// A closed vocabulary is the one thing the bundle enumerates.
//
// Everything else the surface can state about itself, it states in a tool
// description, and the bundle routes to it. Four vocabularies are the
// exception, because an agent needs them *before* a description is
// useful: they are what it chooses between when writing its first
// declaration, and a round trip to discover the list of field types is a
// round trip in the wrong place.
//
// The price of enumerating them is that every one is a second copy of a
// list that lives in Go, so every one is carried in a delimited fence and
// set-compared against its declaration on every build:
//
//	```vocab:field_types
//	text longtext number bool enum list<text>
//	```
//
// The comparison runs in both directions — a word in the code the bundle
// lacks, and a word in the bundle the code lacks — and refuses to run
// over an empty set, because two empty sets compare equal and that is how
// a guard of this shape goes quiet.

// VocabFence is one delimited vocabulary found in the bundle.
type VocabFence struct {
	// Name is the fence's vocabulary name: "field_types" for
	// ```vocab:field_types.
	Name string
	Path string
	Line int
	// Words are the vocabulary's members, in the order they are written.
	// Whitespace separated, so a fence may wrap.
	Words []string
}

// VocabFences reads every `vocab:` fence in the bundle.
//
// It walks the whole tree rather than a list of pages: a vocabulary
// enumerated on a page added tomorrow is exactly as much of a second copy
// as one enumerated today, and a guard that watches the files it was
// written for stops watching the moment somebody adds one.
//
// A fence with no name (```vocab:) and a fence that is never closed are
// both errors rather than silently skipped: an unnamed or unterminated
// fence is an enumeration wearing the costume of a checked one.
func VocabFences(fsys fs.FS) ([]VocabFence, error) {
	var out []VocabFence
	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		fences, err := fencesIn(name, string(body))
		if err != nil {
			return err
		}
		out = append(out, fences...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

func fencesIn(path, body string) ([]VocabFence, error) {
	var out []VocabFence
	var open *VocabFence
	for i, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if open != nil {
			if strings.HasPrefix(trimmed, "```") {
				out = append(out, *open)
				open = nil
				continue
			}
			open.Words = append(open.Words, strings.Fields(trimmed)...)
			continue
		}
		if !strings.HasPrefix(trimmed, "```vocab") {
			continue
		}
		name := strings.TrimPrefix(trimmed, "```vocab")
		name = strings.TrimPrefix(name, ":")
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("%s:%d: a vocab fence with no name: an enumeration no "+
				"guard can be pointed at is an unchecked list wearing a checked list's costume",
				path, i+1)
		}
		open = &VocabFence{Name: name, Path: path, Line: i + 1}
	}
	if open != nil {
		return nil, fmt.Errorf("%s:%d: the vocab:%s fence is never closed", path, open.Line, open.Name)
	}
	return out, nil
}

// VocabWords returns the words of the single fence with this name, and
// reports whether exactly one such fence exists.
//
// Exactly one, deliberately. Two fences for one vocabulary is two copies
// again, and a guard comparing "the" fence would compare one of them and
// leave the other to drift.
func VocabWords(fences []VocabFence, name string) ([]string, error) {
	var found []VocabFence
	for _, fence := range fences {
		if fence.Name == name {
			found = append(found, fence)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("the bundle carries no vocab:%s fence, so the comparison "+
			"against it would run over an empty set and pass", name)
	case 1:
		if len(found[0].Words) == 0 {
			return nil, fmt.Errorf("%s:%d: the vocab:%s fence is empty",
				found[0].Path, found[0].Line, name)
		}
		return found[0].Words, nil
	default:
		places := make([]string, 0, len(found))
		for _, fence := range found {
			places = append(places, fmt.Sprintf("%s:%d", fence.Path, fence.Line))
		}
		return nil, fmt.Errorf("the bundle carries %d vocab:%s fences (%s): one vocabulary, "+
			"one fence, or the copies drift apart", len(found), name, strings.Join(places, ", "))
	}
}

// DiffVocabularies compares a fence's words against a Go declaration and
// returns what each side is missing: words the code declares and the
// bundle does not list, and words the bundle lists and the code does not
// declare.
//
// It is one function rather than two comparisons written at each call
// site because both directions matter and only one of them is ever
// remembered. The bundle missing a word is a value an agent never learns
// exists; the bundle carrying an extra one is a value an agent sends and
// the server refuses.
func DiffVocabularies(bundle, code []string) (missing, extra []string) {
	inBundle := map[string]bool{}
	for _, word := range bundle {
		inBundle[word] = true
	}
	inCode := map[string]bool{}
	for _, word := range code {
		inCode[word] = true
	}
	for _, word := range code {
		if !inBundle[word] {
			missing = append(missing, word)
		}
	}
	for _, word := range bundle {
		if !inCode[word] {
			extra = append(extra, word)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

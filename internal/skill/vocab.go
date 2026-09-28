package skill

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// A closed vocabulary is the one thing the bundle enumerates.
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

package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// linkRE is every href and src the generated pages carry.
var linkRE = regexp.MustCompile(`(?:href|src)="([^"]+)"`)

// checkLinks reports every internal link in the built site that points
// at a file the site does not contain.
func checkLinks(out string) ([]string, error) {
	var pages []string
	err := filepath.WalkDir(out, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".html") {
			pages = append(pages, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", out, err)
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("no page was built in %s, so this check holds nothing", out)
	}

	var broken []string
	for _, page := range pages {
		body, err := os.ReadFile(page) // #nosec G304 -- a path this command just wrote
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", page, err)
		}
		from := filepath.Dir(page)
		for _, match := range linkRE.FindAllStringSubmatch(string(body), -1) {
			target := match[1]
			if external(target) {
				continue
			}
			// An anchor into this same page needs no file; one into
			// another page is checked by its path half.
			clean, _, _ := strings.Cut(target, "#")
			if clean == "" {
				continue
			}
			resolved := filepath.Join(from, filepath.FromSlash(clean))
			//nolint:gosec // A path this command wrote, joined to a link
			// in a page this command rendered: the whole input is the
			// site it just built.
			if _, err := os.Stat(resolved); err != nil {
				rel, _ := filepath.Rel(out, page)
				broken = append(broken, path.Join(filepath.ToSlash(rel))+" → "+target)
			}
		}
	}
	sort.Strings(broken)
	return broken, nil
}

func external(target string) bool {
	lower := strings.ToLower(target)
	return strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "mailto:") ||
		strings.HasPrefix(lower, "//")
}

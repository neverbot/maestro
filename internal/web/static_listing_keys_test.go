package web_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The guard for the defect that killed the Images destination: **the page
// read a key the server has never written.**
//
// `handleListViewAssets` answers `{"assets": […], "next_cursor": "…"}`.
// `pages/assets.js` read `body.items`, which is `undefined`, which is not
// an array, which becomes an empty list, which renders "No images yet" —
// on a game whose map view was drawing one of those files at the time.
// Nothing was red anywhere: the Go tests read `assets` and passed, the
// JavaScript was valid, and the page's own empty state is a legitimate
// thing to render. Every line of row-building underneath that read had
// never executed in a browser, and two of them were separately wrong.
//
// **Why a source guard.** What went wrong can only be seen by rendering
// the page against the real server, and this repository has no browser
// harness. What it can hold is the mechanical precondition: for every
// listing a page reads, the key the page names is a key the handler
// writes. That is the half that regresses when somebody renames a field;
// the browser is what proved the fix.
//
// Mutation: change `body.assets` back to `body.items` in
// `static/pages/assets.js` and this test fails naming the page, the key
// it reads and the keys the handler writes.
func TestListingKeysPagesReadAreKeysHandlersWrite(t *testing.T) {
	t.Parallel()
	// Each entry: the page module, the Go file whose handler answers it,
	// and the reads that must be backed by a written key. A page listing
	// nothing belongs in neither column.
	listings := []struct {
		page    string
		handler string
	}{
		{page: "static/pages/assets.js", handler: "api_view_assets.go"},
	}

	// `writeJSON(w, …, map[string]any{"assets": out, "next_cursor": …})`
	// and the struct tags beside it: every JSON key the handler spells.
	writtenKey := regexp.MustCompile(`"([a-z_]+)":`)
	// `body.assets`, `answer.result.items`: the keys a page reads off a
	// listing envelope, and only those — a read of a field on one item
	// (`asset.filename`) is not a listing key.
	readKey := regexp.MustCompile(`\bbody\.([a-z_]+)\b`)

	for _, listing := range listings {
		pageSrc, err := os.ReadFile(filepath.FromSlash(listing.page))
		if err != nil {
			t.Fatalf("reading %s: %v", listing.page, err)
		}
		handlerSrc, err := os.ReadFile(filepath.FromSlash(listing.handler))
		if err != nil {
			t.Fatalf("reading %s: %v", listing.handler, err)
		}

		written := map[string]bool{}
		for _, m := range writtenKey.FindAllStringSubmatch(string(handlerSrc), -1) {
			written[m[1]] = true
		}

		reads := map[string]bool{}
		for _, m := range readKey.FindAllStringSubmatch(withoutComments(string(pageSrc)), -1) {
			reads[m[1]] = true
		}
		if len(reads) == 0 {
			t.Errorf("%s reads no listing key at all, so this guard is holding nothing", listing.page)
			continue
		}

		for key := range reads {
			if written[key] {
				continue
			}
			have := make([]string, 0, len(written))
			for k := range written {
				have = append(have, k)
			}
			t.Errorf("%s reads body.%s, which %s never writes; it writes %s",
				listing.page, key, listing.handler, strings.Join(have, ", "))
		}
	}
}

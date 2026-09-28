package web_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

// The guard for the defect that killed the Images destination: **the page
// read a key the server has never written.**
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
		assert.Must(t, err == nil, "reading %s: %v", listing.page, err)
		handlerSrc, err := os.ReadFile(filepath.FromSlash(listing.handler))
		assert.Must(t, err == nil, "reading %s: %v", listing.handler, err)

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

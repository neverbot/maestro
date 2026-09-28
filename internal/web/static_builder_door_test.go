package web_test

import (
	"os"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

// TestTheBuilderDoorIsDecidedFromTheStoredQuery is a source guard over
// one call, and it exists because the thing it protects is a promise
// rather than a behaviour anything else can see.
func TestTheBuilderDoorIsDecidedFromTheStoredQuery(t *testing.T) {
	t.Parallel()
	for _, page := range []struct {
		path string
		what string
	}{
		{"static/pages/view.js", "the view page decides whether to offer the way in"},
		{"static/pages/builder.js", "the builder decides whether to open what it was pointed at"},
	} {
		source, err := os.ReadFile(page.path)
		assert.Must(t, err == nil, "read %s: %v", page.path, err)
		text := string(source)
		assert.Should(t, strings.Contains(text, "roundTrips("), "%s never calls roundTrips: %s, and §4 says that decision is made against "+
			"the stored bytes every time", page.path, page.what)
		assert.Should(t, strings.Contains(text, "TOO_MUCH"), "%s never says TOO_MUCH: a query the builder cannot hold has to be explained "+
			"in the spike's own words rather than by a control that is simply missing", page.path)
	}
}

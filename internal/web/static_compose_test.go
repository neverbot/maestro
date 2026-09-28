package web_test

import (
	"bufio"
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/views"
)

// TestComposingAQuery drives internal/web/jstest/compose_test.mjs.
func TestComposingAQuery(t *testing.T) {
	t.Parallel()
	runJSTest(t, "jstest/compose_test.mjs")
}

// TestEveryDocumentTheBuilderEmitsParses is the call-site half, and it
// is the one that matters.
func TestEveryDocumentTheBuilderEmitsParses(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("node", "jstest/compose_test.mjs", "--emit").Output()
	assert.Must(t, err == nil, "running the emitter harness: %v", err)

	documents := 0
	scan := bufio.NewScanner(bytes.NewReader(out))
	scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}
		documents++
		if _, err := views.ParseQuery([]byte(line)); err != nil {
			t.Errorf("the server refuses a document the builder emits:\n  %s\n  %v", line, err)
		}
	}
	if err := scan.Err(); err != nil {
		t.Fatalf("reading the emitted documents: %v", err)
	}
	// A harness that emitted nothing would pass this test in silence,
	// which is the same failure as having no test at all.
	assert.Must(t, documents >= 5, "the harness emitted %d documents; it composes more than that", documents)
}

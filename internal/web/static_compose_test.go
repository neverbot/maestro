package web_test

import (
	"bufio"
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/views"
)

// TestComposingAQuery drives internal/web/jstest/compose_test.mjs.
//
// The emitter is the half of the query builder that can be proven
// without a browser: a clause stack in, a query document out, and the
// map from each control to the JSON pointer it wrote, which is what lets
// a validate diagnostic land on the line of the sentence that caused it
// rather than in a box at the bottom of the page.
func TestComposingAQuery(t *testing.T) {
	runJSTest(t, "jstest/compose_test.mjs")
}

// TestEveryDocumentTheBuilderEmitsParses is the call-site half, and it
// is the one that matters.
//
// The harness above proves the emitter is self-consistent: what it
// writes, it reads back. That is exactly the shape of the defect this
// repository has met most often — a unit asserted by a harness that
// calls it directly, wired to nothing. The server's own parser is the
// only judge of whether a document the builder emits is a query at all,
// so every document the harness composes is fed to views.ParseQuery
// here. A sentence a person can compose whose document the server
// refuses is a builder that produces refusals.
//
// Parsing, not resolving: ParseQuery judges shape and bounds, and
// nothing in it knows whether `quest` is a type this game declared.
// Resolution needs a game, and what is being checked here is the
// emitter's output rather than the vocabulary the pickers supplied.
func TestEveryDocumentTheBuilderEmitsParses(t *testing.T) {
	out, err := exec.Command("node", "jstest/compose_test.mjs", "--emit").Output()
	if err != nil {
		t.Fatalf("running the emitter harness: %v", err)
	}

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
	if documents < 5 {
		t.Fatalf("the harness emitted %d documents; it composes more than that", documents)
	}
}

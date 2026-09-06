package web_test

import (
	"os"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/views"
)

// The source-shape guards over
// internal/web/static/components/mst-ground.js — the upload picker.
//
// The property they hold has no runtime signature any harness can see.
// The picker states the refusal *before* a file is chosen, which means it
// states it from constants of its own rather than from anything the
// server said on this request: a picker cannot ask what will be refused
// without uploading something first. So the three facts are a second copy
// of a rule that lives in internal/views, and a second copy of a rule is
// exactly what this repository's standing failure list calls drift.
//
// internal/web/jstest/writes_test.mjs asserts the three facts reach the
// DOM with no file selected. These assert that what they say is what the
// server will actually do.

const groundModule = "static/components/mst-ground.js"

// TestTheUploadPickerStatesTheServersOwnBounds pins the picker's promises
// to the values the domain enforces.
//
// The byte count is internal/views.MaxAssetBytes and the three formats
// are its MimePNG, MimeJPEG and MimeWebP — the same constants
// readBounded and sniffedMime are written against. Raising the cap on the
// server without touching the picker leaves a designer reading a bound
// that is no longer true, which is a worse failure than no bound at all:
// the sentence looks authoritative.
func TestTheUploadPickerStatesTheServersOwnBounds(t *testing.T) {
	raw, err := os.ReadFile(groundModule)
	if err != nil {
		t.Fatalf("read %s: %v", groundModule, err)
	}
	source := string(raw)
	if len(source) < 500 {
		t.Fatalf("%s is %d bytes: this test would pass on an empty file", groundModule, len(source))
	}
	for _, want := range []struct {
		value  string
		reason string
	}{
		{"${MAX_ASSET_BYTES}", "the size bound, interpolated rather than typed out a second time"},
		{views.MimePNG, "one of the three mimes internal/views.sniffedMime admits"},
		{views.MimeJPEG, "one of the three mimes internal/views.sniffedMime admits"},
		{views.MimeWebP, "one of the three mimes internal/views.sniffedMime admits"},
	} {
		if !strings.Contains(source, want.value) {
			t.Errorf("%s never names %q, which is %s: the picker states the refusal before a file is "+
				"chosen, and it may only state what the server will really do",
				groundModule, want.value, want.reason)
		}
	}
	// The SVG refusal is the one a designer cannot guess, and the reason
	// is the whole of it: an SVG served to a browser can carry script.
	// A picker that said only "SVG is not supported" would leave them
	// looking for the setting that turns it on.
	for _, phrase := range []string{"SVG", "script"} {
		if !strings.Contains(source, phrase) {
			t.Errorf("%s never says %q: internal/views refuses an SVG because one served to a browser "+
				"can carry script, and repeating the reason is what stops a designer hunting for a setting",
				groundModule, phrase)
		}
	}
}

// TestTheGroundComponentReadsTheAssetCapItPromises is the guard on the
// guard above, in the direction that one cannot fail: it asserts the
// picker's own constant equals the domain's number, rather than merely
// appearing somewhere in the file. A file that happened to contain the
// digits in a comment would satisfy a substring scan and promise nothing.
func TestTheGroundComponentReadsTheAssetCapItPromises(t *testing.T) {
	raw, err := os.ReadFile(groundModule)
	if err != nil {
		t.Fatalf("read %s: %v", groundModule, err)
	}
	const declaration = "export const MAX_ASSET_BYTES = 8 << 20;"
	if !strings.Contains(string(raw), declaration) {
		t.Fatalf("%s does not declare %s: the picker's cap is internal/views.MaxAssetBytes, which is "+
			"%d, and it is declared in the same arithmetic so the two are read as one number",
			groundModule, declaration, views.MaxAssetBytes)
	}
	if views.MaxAssetBytes != 8<<20 {
		t.Fatalf("internal/views.MaxAssetBytes is %d and %s still promises %d",
			views.MaxAssetBytes, groundModule, 8<<20)
	}
}

package web_test

import "testing"

// TestTheListingRows drives internal/web/jstest/rows_test.mjs.
//
// rows.js draws every listing in the product, and its header stopped
// being a label when the catalogue's columns became the way a person
// orders a thousand rows. What the harness holds is the half of a sort
// that can be wrong while looking right: which order each press asks
// for, that a column with no server-side order carries no control, and
// that the direction is in words and not only in an arrow.
func TestTheListingRows(t *testing.T) {
	runJSTest(t, "jstest/rows_test.mjs")
}

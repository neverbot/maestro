package web_test

import "testing"

// TestThePickerSources drives internal/web/jstest/pickers_test.mjs.
//
// `mst-picker` is the control and static_picker_test.go holds it; this
// is the half that decides **what may be chosen**. Three of the four
// pickers are a game's own vocabulary — its entity types, its relation
// types, the fields one type declares — and the fourth cannot be a list
// at all: a game in this instance holds a thousand entities of one type,
// so the entity picker is a search, and what it has to get right is when
// it asks and what it does with an answer that arrives after the
// question has changed.
func TestThePickerSources(t *testing.T) {
	runJSTest(t, "jstest/pickers_test.mjs")
}

package web

// The test hooks for the generated tool index. They live in their own
// file rather than in export_test.go for the same reason the guards live
// in their own file: the bundle's tests are a growing set with several
// tasks still to land on them, and a shared file every one of them edits
// is a shared file every one of them conflicts in.

// ToolReferenceFromForTest renders the index from a hand-built tool
// table. It exists so the grouping and the first-sentence rules can be
// driven with inputs the real surface does not contain — an
// unprefixed tool name, a description whose first sentence ends in an
// abbreviation — which is what stops those rules from being asserted
// only by the output they themselves produced.
func ToolReferenceFromForTest(descriptions map[string]string) string {
	return toolReference(descriptions)
}

// FirstSentenceForTest is the sentence-splitting rule the index is built
// on, exposed so its edge cases can be named one by one.
func FirstSentenceForTest(text string) string {
	return firstSentence(text)
}

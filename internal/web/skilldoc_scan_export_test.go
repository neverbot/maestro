package web

import "io/fs"

// The test hooks for the anti-restatement scanner. The scanner itself is
// ordinary package code — the bundle's pages are written against its
// notion of a sentence, so it must be readable by whoever writes one —
// and these are only what lets the guard be driven with hand-written
// inputs the real bundle does not contain.

// ScannedSentence is bundleSentence, exported for the guard's tests.
type ScannedSentence struct {
	Path     string
	Line     int
	Text     string
	InQuote  bool
	QuoteFor string
}

func exportSentences(in []bundleSentence) []ScannedSentence {
	out := make([]ScannedSentence, 0, len(in))
	for _, s := range in {
		out = append(out, ScannedSentence{
			Path: s.Path, Line: s.Line, Text: s.Text, InQuote: s.InQuote, QuoteFor: s.QuoteFor,
		})
	}
	return out
}

func importSentences(in []ScannedSentence) []bundleSentence {
	out := make([]bundleSentence, 0, len(in))
	for _, s := range in {
		out = append(out, bundleSentence{
			Path: s.Path, Line: s.Line, Text: s.Text, InQuote: s.InQuote, QuoteFor: s.QuoteFor,
		})
	}
	return out
}

// ScanBundleForTest splits a whole bundle tree into sentences.
func ScanBundleForTest(fsys fs.FS) ([]ScannedSentence, error) {
	sentences, err := scanBundle(fsys)
	if err != nil {
		return nil, err
	}
	return exportSentences(sentences), nil
}

// ScanMarkdownForTest splits one page, so the splitter's own edge cases
// — a wrapped sentence, a bullet, a table cell, a forged attribution —
// can be named one at a time.
func ScanMarkdownForTest(name, body string) []ScannedSentence {
	return exportSentences(scanMarkdown(name, body))
}

// ScanJSONForTest splits one transcript's string values.
func ScanJSONForTest(name string, body []byte) ([]ScannedSentence, error) {
	sentences, err := scanJSON(name, body)
	if err != nil {
		return nil, err
	}
	return exportSentences(sentences), nil
}

// AuditRestatementForTest applies the one-tool test to a set of
// sentences against a tool table, returning one rendered violation per
// crossing.
func AuditRestatementForTest(sentences []ScannedSentence, registered map[string]string) []string {
	found := auditRestatement(importSentences(sentences), registered)
	out := make([]string, 0, len(found))
	for _, violation := range found {
		out = append(out, violation.String())
	}
	return out
}

// ClaimMarkersForTest is the closed marker list, exposed so a test can
// assert the mutation that empties it is caught.
func ClaimMarkersForTest() []string { return claimMarkers }

// SplitSentencesForTest is the sentence rule on its own.
func SplitSentencesForTest(fragment string) []string { return splitSentences(fragment) }

// ToolsNamedInForTest is the whole-token tool matcher, exposed because
// its prefix behaviour — views.list_assets is not a mention of
// views.list — is the exact shape of four broken guards in this
// repository's history.
func ToolsNamedInForTest(text string, registered map[string]string) []string {
	return toolsNamedIn(text, registered)
}

// BundleToolToken is toolToken, exported for the guards.
type BundleToolToken struct {
	Path string
	Line int
	Name string
}

// BundleToolTokensForTest collects every backticked, tool-shaped token
// in a bundle tree.
func BundleToolTokensForTest(fsys fs.FS) ([]BundleToolToken, error) {
	tokens, err := bundleToolTokens(fsys)
	if err != nil {
		return nil, err
	}
	out := make([]BundleToolToken, 0, len(tokens))
	for _, token := range tokens {
		out = append(out, BundleToolToken{Path: token.Path, Line: token.Line, Name: token.Name})
	}
	return out, nil
}

// BundleToolMentionsForTest finds every mention of a registered tool,
// including the unprefixed ones a dotted-token scan cannot see.
func BundleToolMentionsForTest(fsys fs.FS, registered map[string]string) ([]BundleToolToken, error) {
	tokens, err := bundleToolMentions(fsys, registered)
	if err != nil {
		return nil, err
	}
	out := make([]BundleToolToken, 0, len(tokens))
	for _, token := range tokens {
		out = append(out, BundleToolToken{Path: token.Path, Line: token.Line, Name: token.Name})
	}
	return out, nil
}

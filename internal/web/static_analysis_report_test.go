package web_test

import "testing"

// TestTheAnalysisReportsSentences drives
// internal/web/jstest/analysis_report_test.mjs, which reads the verdict,
// the run line and the gate line against the envelope the engine really
// sends.
//
// **Why the harness and not a Go test.** Nothing here is a server
// property: every defect these three sentences have shipped was a
// reading defect over a correct payload. The verdict was assembled so
// that only the half saying "nothing is wrong" could survive, and a
// game with loops of both kinds rendered no verdict at all. The run line
// labelled a seed-row count "entities", so the cycles report said 56 on
// a game whose two neighbouring reports said 28. The gate line tested
// `derived_from_role`, a field nothing sends, which made the resolver's
// own "a mechanism nothing reads is a lie" comment describe the screen
// that was meant to prove it. A Go test over the API would have passed
// through all three.
func TestTheAnalysisReportsSentences(t *testing.T) {
	t.Parallel()
	runJSTest(t, "jstest/analysis_report_test.mjs")
}

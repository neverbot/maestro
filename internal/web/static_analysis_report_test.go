package web_test

import "testing"

// TestTheAnalysisReportsSentences drives
// internal/web/jstest/analysis_report_test.mjs, which reads the verdict,
// the run line and the gate line against the envelope the engine really
// sends.
func TestTheAnalysisReportsSentences(t *testing.T) {
	t.Parallel()
	runJSTest(t, "jstest/analysis_report_test.mjs")
}

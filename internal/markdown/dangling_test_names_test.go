package markdown_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// forwardReferencedTestNames names tests a comment in this package cites
// as covering a claim, but that do not exist yet because the task that
// will add them has not landed. TestNoCommentNamesATestThatDoesNotExist
// below would otherwise flag these as dangling, which is the wrong
// diagnosis for a forward reference honestly labelled as one -- the
// defect this check exists for is a citation that reads as present
// coverage and is not, not a plan naming work still to come. Each entry
// is removed the day the task lands and the named test exists; if a
// removal is forgotten, the standing check starts failing for the right
// reason (an entry that is no longer forward, just wrong).
// It is empty today: Task 10 landed internal/web's arm for
// markdown.ConflictError together with
// TestEveryMarkdownDomainErrorHasAWireCode, which was this map's only
// entry, so the citation in errors.go now names a test that exists and
// the exception came out with it.
var forwardReferencedTestNames = map[string]string{}

// renamedAwayTestNames names tests a comment cites as a *former* name of
// a test that exists today under a different one — the opposite
// direction from forwardReferencedTestNames, found the day widening this
// check to scan internal/metamodel (review of Task 7 of the markdown
// plan) turned up a real instance: search_test.go narrates that
// TestSearchRanksTheNameMatchFirst "used to be
// TestSearchRanksTheStrongerMatchFirst", which is history, not a claim
// that the old name is still coverage anywhere. That reads exactly like
// a dangling citation to this check's regex, which cannot tell "used to
// be" from "covers this" apart — so the distinction is made here,
// explicitly, rather than by weakening the regex to parse tense, which
// would just move the false-negative risk onto every other comment in
// the sweep.
var renamedAwayTestNames = map[string]string{
	"TestSearchRanksTheStrongerMatchFirst": "internal/metamodel/search_test.go: " +
		"the pre-Task-7 name of TestSearchRanksTheNameMatchFirst, cited only " +
		"to say what changed and why.",
	"TestAVersionClaimAgainstAMissingTypeCreatesItRatherThanRefusing": "internal/metamodel/" +
		"removed_test.go: the test that pinned the *opposite* outcome — a version claim " +
		"reaching an upsert's insert path creating the row — which Metamodel 17 replaced " +
		"with TestAVersionClaimAgainstATypeThisGameNeverHadIsRefusedToo. It is cited to " +
		"say which decision was reversed and why, which is history rather than a claim " +
		"that the old name is coverage anywhere.",
}

// TestNoCommentNamesATestThatDoesNotExist is the standing check the
// review of Task 6 asked for, after a second round of dangling test
// names found by hand in this package and in
// internal/db/queries/documents.sql -- one comment citing a test that
// existed nowhere in the module, another citing a real test as covering
// a claim it did not cover. Neither is a mistake a reviewer reliably
// catches by re-reading: a cited name reads exactly like a real one, so
// this greps for it instead.
//
// It builds the set of every top-level test function this module
// actually defines, then scans every comment in internal/markdown's own
// Go files, in internal/metamodel's own Go files and in
// internal/db/queries/*.sql (the places this domain's commentary lives)
// for something shaped like a test name and fails on any that names no
// test in that set, forwardReferencedTestNames excepted. A cited test
// may live in another package entirely still (this file's own sibling,
// versions.go, names a paging package test pinning the clamp policy
// History reuses), so the defined set is built from the whole module,
// not just the packages scanned.
//
// **internal/metamodel was added here, not just internal/markdown and
// internal/db/queries, because a citation already lived there
// uncovered.** internal/metamodel/keys.go:145's comment on
// RowKeyProblems cites TestAnEntityAddressIsBoundedBeforePostgresSeesIt
// — a real test, in this package — but until this line named that
// directory, this check could not have told that citation apart from a
// dangling one had the cited test been renamed or removed. Task 7 is
// what made internal/markdown cite into internal/metamodel; the check's
// coverage had drifted from where this domain's citations actually live
// from that point on, and it is widened here rather than left for
// Task 8, which adds more of both.
//
// **internal/web was added here for the same reason, in Task 9's
// review.** mcp_search.go and mcp_metamodel.go both cite this domain's
// own tests by name — every one of them resolved, checked by hand, but
// only because a reviewer looked — and Task 10's mcp_docs.go is about to
// carry far more of that citation traffic than search.go alone does.
// Widening the scan now, rather than after Task 10 lands, is cheaper
// than finding a dangler by hand a second time.
//
// **internal/web/static was added here in Task 12**, which is the last
// place this domain's commentary lives and the last one this check could
// not see. doc.js's own header cites TestAppScriptNeverWritesRawHTML and
// TestTheDocumentScriptHasExactlyOneHTMLSink as the tests holding the
// page's one-sink rule; both resolve today, and adding the directory
// while they do is exactly when this costs nothing. `.js` and `.mjs`
// are read with the `//` prefix, `.html` with `<!--` (the trailing
// `-->` is left on the comment text, which the name pattern simply does
// not match). The scan stays one directory deep, as it is for every
// other entry above, so a nested asset directory added later needs a
// line here.
func TestNoCommentNamesATestThatDoesNotExist(t *testing.T) {
	root := moduleRoot(t)
	defined := definedTestNames(t, root)

	var dangling []string
	for _, dir := range []string{
		filepath.Join(root, "internal", "markdown"),
		filepath.Join(root, "internal", "metamodel"),
		filepath.Join(root, "internal", "web"),
		filepath.Join(root, "internal", "db", "queries"),
		filepath.Join(root, "internal", "web", "static"),
	} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			var prefix string
			switch {
			case strings.HasSuffix(name, ".go"):
				prefix = "//"
			case strings.HasSuffix(name, ".sql"):
				prefix = "--"
			case strings.HasSuffix(name, ".js"), strings.HasSuffix(name, ".mjs"):
				prefix = "//"
			case strings.HasSuffix(name, ".html"):
				prefix = "<!--"
			default:
				continue
			}
			path := filepath.Join(dir, name)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			for _, d := range citedTestNames(string(raw), prefix) {
				if defined[d.name] || forwardReferencedTestNames[d.name] != "" ||
					renamedAwayTestNames[d.name] != "" {
					continue
				}
				dangling = append(dangling, path+":"+strconv.Itoa(d.line)+
					": cites "+d.name+", which no test in the module defines")
			}
		}
	}

	if len(dangling) > 0 {
		sort.Strings(dangling)
		t.Fatalf("%d dangling test name(s) found:\n%s",
			len(dangling), strings.Join(dangling, "\n"))
	}
}

var testNameRE = regexp.MustCompile(`\bTest[A-Z][A-Za-z0-9_]*\b`)

// wordRE matches the run of identifier characters at the very start of
// a string, used to pull the continuation of a name that a hard line
// wrap split in two. It requires an initial capital: this package's own
// prose is lowercase after a name ("...Unchanged pins both directions",
// "...ContinuesTheNumbering is what pins it"), so a lower-case word at
// the front of the next comment line is the next sentence, not a second
// half of the identifier -- only another capitalised fragment is.
var wordRE = regexp.MustCompile(`^[A-Z][A-Za-z0-9_]*`)

type citation struct {
	name string
	line int // 1-based, the line the citation starts on
}

// citedTestNames extracts every Test-shaped identifier from the
// comments of a source file, given the file's line-comment prefix ("//"
// or "--").
//
// **Long identifiers get hard-wrapped mid-word, with no space and no
// hyphen** — this package's own comments do it (see
// internal/db/queries/documents.sql, where one test name spans two "--"
// lines) — because the wrapping is done by an author holding an 80-odd
// column budget, not by a tool that knows an identifier from a word. A
// naive per-line scan reads the two halves as two short, unrelated,
// nonexistent names and reports both as dangling, which is a false
// positive this check must not have or nobody will trust its real
// findings. So: a match that reaches the exact end of its line's comment
// text is a candidate fragment, and its continuation is pulled off the
// front of the next comment line, with no separator inserted, mirroring
// how the split happened in the first place. A citation that ends
// mid-line, followed by punctuation or more prose on the same line, is
// never treated as a fragment — a name cited mid-sentence reads its
// comment line whole.
func citedTestNames(src, prefix string) []citation {
	lines := strings.Split(src, "\n")
	comments := make([]string, len(lines))
	isComment := make([]bool, len(lines))
	for i, line := range lines {
		idx := strings.Index(line, prefix)
		if idx < 0 {
			continue
		}
		isComment[i] = true
		comments[i] = strings.TrimSpace(line[idx+len(prefix):])
	}

	var out []citation
	for i, text := range comments {
		if !isComment[i] {
			continue
		}
		for _, loc := range testNameRE.FindAllStringIndex(text, -1) {
			name := text[loc[0]:loc[1]]
			line := i + 1
			// A match reaching the end of the comment text may be a
			// fragment: pull continuations off however many following
			// comment lines extend it, directly, with no separator.
			for loc[1] == len(text) {
				j := i + 1
				for {
					if j >= len(comments) || !isComment[j] {
						loc[1] = -1 // no more comment lines to pull from
						break
					}
					if comments[j] == "" {
						j++ // a blank comment line does not end a wrap
						continue
					}
					break
				}
				if loc[1] == -1 {
					break
				}
				cont := wordRE.FindString(comments[j])
				if cont == "" {
					break
				}
				name += cont
				text = comments[j]
				i = j
				loc[1] = len(cont)
				if loc[1] != len(text) {
					break
				}
			}
			out = append(out, citation{name: name, line: line})
		}
	}
	return out
}

// definedTestNames walks every *_test.go file in the module and returns
// the set of every top-level test function it declares.
func definedTestNames(t *testing.T, root string) map[string]bool {
	t.Helper()
	defFuncRE := regexp.MustCompile(`(?m)^func\s+(Test[A-Z][A-Za-z0-9_]*)\s*\(`)
	defined := make(map[string]bool)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "vendor" || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range defFuncRE.FindAllStringSubmatch(string(raw), -1) {
			defined[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return defined
}

// moduleRoot finds the repository root by walking up from this file's
// own location to the directory holding go.mod, rather than assuming
// `go test`'s working directory, which callers may change.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above " + file)
		}
		dir = parent
	}
}

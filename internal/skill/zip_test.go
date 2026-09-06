package skill

import (
	"archive/zip"
	"bytes"
	"io"
	"io/fs"
	"testing"
	"testing/fstest"
)

// TestTheZipIsByteIdenticalAcrossCalls pins the property bundle_version
// is only useful under: the same tree packs to the same bytes, so an
// agent that has already downloaded a version can skip the next
// download.
//
// Mutation that proves it bites: set the entry header's Modified to
// time.Now() in zipTree (or build the entry with w.Create, which does
// exactly that) and this test fails on the second call.
func TestTheZipIsByteIdenticalAcrossCalls(t *testing.T) {
	first, err := Zip()
	if err != nil {
		t.Fatalf("Zip: %v", err)
	}
	second, err := Zip()
	if err != nil {
		t.Fatalf("Zip: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("two calls to Zip produced different bytes: %d and %d bytes", len(first), len(second))
	}
	if len(first) == 0 {
		t.Fatal("Zip returned no bytes: two empty archives are byte-identical for the wrong reason")
	}
}

// TestTheZipCarriesNoBuildTimestamp is the other half of determinism,
// and it exists because the obvious half is not enough: a zip built with
// time.Now() in its headers is still byte-identical across two calls
// made in the same second, so TestTheZipIsByteIdenticalAcrossCalls above
// passes under precisely the defect it looks like it catches. What makes
// bundle_version worth anything is that two *builds*, days apart, of the
// same tree agree — which is a property of the stored stamp, not of two
// calls in one process.
//
// Mutation that proves it bites: set Modified: time.Now() on the header
// in zipTree; this test fails naming the entry and the year, while the
// byte-equality test stays green.
func TestTheZipCarriesNoBuildTimestamp(t *testing.T) {
	archive, err := Zip()
	if err != nil {
		t.Fatalf("Zip: %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("reading the archive back: %v", err)
	}
	if len(reader.File) == 0 {
		t.Fatal("the archive holds no entries: there are no timestamps here to be wrong")
	}
	for _, entry := range reader.File {
		// A zeroed FileHeader.Modified is written as the zip format's
		// own zero stamp, which decodes to 1979-11-30. Any real build
		// clock lands decades later.
		if year := entry.Modified.Year(); year > 1980 {
			t.Errorf("%s carries a modification time of %s: the archive is stamped with the build clock, so two builds of one tree differ",
				entry.Name, entry.Modified.UTC().Format("2006-01-02 15:04:05"))
		}
	}
}

// TestTheZipHoldsExactlyTheTree is what makes hashing the tree rather
// than the archive honest. Version covers the tree; an agent is served
// the archive; this is the assertion that the two hold the same files
// with the same bytes, so a hash that moves is a download that moved.
func TestTheZipHoldsExactlyTheTree(t *testing.T) {
	archive, err := Zip()
	if err != nil {
		t.Fatalf("Zip: %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("reading the archive back: %v", err)
	}
	packed := map[string][]byte{}
	for _, entry := range reader.File {
		rc, err := entry.Open()
		if err != nil {
			t.Fatalf("opening %s: %v", entry.Name, err)
		}
		body, err := io.ReadAll(rc)
		if closeErr := rc.Close(); closeErr != nil {
			t.Fatalf("closing %s: %v", entry.Name, closeErr)
		}
		if err != nil {
			t.Fatalf("reading %s: %v", entry.Name, err)
		}
		if _, seen := packed[entry.Name]; seen {
			t.Fatalf("%s appears twice in the archive", entry.Name)
		}
		packed[entry.Name] = body
	}

	tree := treeOf(t, Files())
	if len(tree) == 0 {
		t.Fatal("the bundle tree is empty: this test would pass by comparing two empty sets")
	}
	for path, body := range tree {
		got, ok := packed[path]
		if !ok {
			t.Errorf("%s is in the tree and not in the archive", path)
			continue
		}
		if !bytes.Equal(got, body) {
			t.Errorf("%s differs between the tree and the archive", path)
		}
		delete(packed, path)
	}
	for path := range packed {
		t.Errorf("%s is in the archive and not in the tree", path)
	}
}

// TestTheVersionHashIsLengthPrefixed is the assertion that would be
// missing if the test only checked that the real bundle hashes stably,
// which is true under a broken hash too.
//
// Mutation that proves it bites: drop the length prefixes from hashTree
// (write the path and the body straight into the digest) and the two
// trees below hash identically.
func TestTheVersionHashIsLengthPrefixed(t *testing.T) {
	left, err := hashTree(fstest.MapFS{"ab": &fstest.MapFile{Data: []byte("c")}})
	if err != nil {
		t.Fatalf("hashTree: %v", err)
	}
	right, err := hashTree(fstest.MapFS{"a": &fstest.MapFile{Data: []byte("bc")}})
	if err != nil {
		t.Fatalf("hashTree: %v", err)
	}
	if left == right {
		t.Fatalf("{\"ab\": \"c\"} and {\"a\": \"bc\"} hash the same (%s): the hash concatenates without length prefixes", left)
	}
}

// TestTheVersionMovesWhenOneByteMoves drives the real tree, not a
// fixture, so a hash that somehow read something other than the bundle
// would be caught here.
func TestTheVersionMovesWhenOneByteMoves(t *testing.T) {
	before, err := hashTree(Files())
	if err != nil {
		t.Fatalf("hashTree: %v", err)
	}
	if before != Version() {
		t.Fatalf("Version() is %s and hashing the tree gives %s: Version does not hash what ships", Version(), before)
	}
	after, err := hashTree(mutated(t, "skill.md", func(body []byte) []byte { return append(body, '.') }))
	if err != nil {
		t.Fatalf("hashTree: %v", err)
	}
	if before == after {
		t.Fatalf("appending one byte to skill.md left the version at %s", before)
	}
}

// TestTheVersionMovesWheneverTheZipDoes is the guard on the decision to
// hash the tree rather than the bytes an agent downloads. It partitions
// a set of mutated trees both ways and asserts the partitions agree: no
// two trees may pack to different archives and hash the same, and none
// may pack identically and hash apart.
//
// Mutation that proves it bites: make zipTree skip a path (say, one
// ending in ".md"); two trees differing only in skill.md then pack
// identically while hashing apart, and this test names the pair.
func TestTheVersionMovesWheneverTheZipDoes(t *testing.T) {
	trees := map[string]fs.FS{
		"as it ships":       Files(),
		"one byte appended": mutated(t, "skill.md", func(body []byte) []byte { return append(body, '.') }),
		"one byte removed":  mutated(t, "skill.md", func(body []byte) []byte { return body[:len(body)-1] }),
		"a file removed":    mutated(t, "skill.md", nil),
		"a file added":      added(t, "notes.md", "a page a contributor added"),
		"a file renamed":    renamed(t, "skill.md", "skill-renamed.md"),
	}
	type shape struct{ zip, version string }
	shapes := map[string]shape{}
	for name, tree := range trees {
		archive, err := zipTree(tree)
		if err != nil {
			t.Fatalf("zipTree(%s): %v", name, err)
		}
		sum, err := hashTree(tree)
		if err != nil {
			t.Fatalf("hashTree(%s): %v", name, err)
		}
		shapes[name] = shape{zip: string(archive), version: sum}
	}
	for leftName, left := range shapes {
		for rightName, right := range shapes {
			if leftName >= rightName {
				continue
			}
			sameZip := left.zip == right.zip
			sameVersion := left.version == right.version
			if sameZip != sameVersion {
				t.Errorf("%q and %q: same archive = %v but same version = %v — the version does not cover what is served",
					leftName, rightName, sameZip, sameVersion)
			}
		}
	}
}

// treeOf reads a whole bundle tree into memory: path to body.
func treeOf(t *testing.T, fsys fs.FS) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		body, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		out[path] = body
		return nil
	})
	if err != nil {
		t.Fatalf("walking the bundle: %v", err)
	}
	return out
}

// overlay copies the real bundle into an in-memory tree a test can edit
// without touching the files on disk. Every mutation helper below goes
// through it, which is why no test in this package writes to the
// repository.
func overlay(t *testing.T) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	for path, body := range treeOf(t, Files()) {
		out[path] = &fstest.MapFile{Data: body}
	}
	if len(out) == 0 {
		t.Fatal("the bundle is empty: every overlay built from it would be empty too")
	}
	return out
}

// mutated returns the bundle with one file rewritten by change, or —
// when change is nil — with that file removed.
func mutated(t *testing.T, path string, change func([]byte) []byte) fstest.MapFS {
	t.Helper()
	out := overlay(t)
	file, ok := out[path]
	if !ok {
		t.Fatalf("%s is not in the bundle: this mutation would be a no-op", path)
	}
	if change == nil {
		delete(out, path)
		return out
	}
	out[path] = &fstest.MapFile{Data: change(append([]byte(nil), file.Data...))}
	return out
}

// added returns the bundle with one extra file in it.
func added(t *testing.T, path, body string) fstest.MapFS {
	t.Helper()
	out := overlay(t)
	if _, exists := out[path]; exists {
		t.Fatalf("%s is already in the bundle: adding it would be a no-op", path)
	}
	out[path] = &fstest.MapFile{Data: []byte(body)}
	return out
}

// renamed returns the bundle with one file moved, its bytes unchanged.
// It is the mutation a hash without path prefixes would not notice.
func renamed(t *testing.T, from, to string) fstest.MapFS {
	t.Helper()
	out := overlay(t)
	file, ok := out[from]
	if !ok {
		t.Fatalf("%s is not in the bundle: this rename would be a no-op", from)
	}
	delete(out, from)
	out[to] = &fstest.MapFile{Data: file.Data}
	return out
}

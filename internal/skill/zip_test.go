package skill

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/neverbot/maestro/internal/assert"
)

// TestTheZipIsByteIdenticalAcrossCalls pins the property bundle_version
// is only useful under: the same tree packs to the same bytes, so an
// agent that has already downloaded a version can skip the next
// download.
func TestTheZipIsByteIdenticalAcrossCalls(t *testing.T) {
	first, err := Zip()
	assert.Must(t, err == nil, "Zip: %v", err)
	second, err := Zip()
	assert.Must(t, err == nil, "Zip: %v", err)
	assert.Must(t, bytes.Equal(first, second), "two calls to Zip produced different bytes: %d and %d bytes", len(first), len(second))
	assert.Must(t, len(first) != 0, "Zip returned no bytes: two empty archives are byte-identical for the wrong reason")
}

// TestTheZipCarriesNoBuildTimestamp is the other half of determinism,
// and it exists because the obvious half is not enough: a zip built with
// time.Now() in its headers is still byte-identical across two calls
// made in the same second, so TestTheZipIsByteIdenticalAcrossCalls above
// passes under precisely the defect it looks like it catches. What makes
// bundle_version worth anything is that two *builds*, days apart, of the
// same tree agree — which is a property of the stored stamp, not of two
// calls in one process.
func TestTheZipCarriesNoBuildTimestamp(t *testing.T) {
	archive, err := Zip()
	assert.Must(t, err == nil, "Zip: %v", err)
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	assert.Must(t, err == nil, "reading the archive back: %v", err)
	assert.Must(t, len(reader.File) != 0, "the archive holds no entries: there are no timestamps here to be wrong")
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
	assert.Must(t, err == nil, "Zip: %v", err)
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	assert.Must(t, err == nil, "reading the archive back: %v", err)
	packed := map[string][]byte{}
	for _, entry := range reader.File {
		rc, err := entry.Open()
		assert.Must(t, err == nil, "opening %s: %v", entry.Name, err)
		body, err := io.ReadAll(rc)
		if closeErr := rc.Close(); closeErr != nil {
			t.Fatalf("closing %s: %v", entry.Name, closeErr)
		}
		assert.Must(t, err == nil, "reading %s: %v", entry.Name, err)
		if _, seen := packed[entry.Name]; seen {
			t.Fatalf("%s appears twice in the archive", entry.Name)
		}
		packed[entry.Name] = body
	}

	tree := treeOf(t, Files())
	assert.Must(t, len(tree) != 0, "the bundle tree is empty: this test would pass by comparing two empty sets")
	// The manifest ships beside the pages and is not one of them; its own
	// contents are asserted by TestTheVersionIsTheHashOfTheFileThatShips.
	assert.Must(t, len(packed[ManifestName]) > 0, "the archive carries no %s", ManifestName)
	delete(packed, ManifestName)
	for path, body := range tree {
		got, ok := packed[path]
		if !ok {
			t.Errorf("%s is in the tree and not in the archive", path)
			continue
		}
		assert.Should(t, bytes.Equal(got, body), "%s differs between the tree and the archive", path)
		delete(packed, path)
	}
	for path := range packed {
		t.Errorf("%s is in the archive and not in the tree", path)
	}
}

// **The manifest separates a path from its content.** Two trees that
// differ only in where one byte sits must not render the same
// SHA256SUMS, which a hash built by concatenation would allow.
// versionOfTree is Version() for a tree a test built, through the same
// manifest the real bundle ships.
func versionOfTree(fsys fs.FS) (string, error) {
	manifest, err := manifestOf(fsys)
	if err != nil {
		return "", err
	}
	return VersionOf(manifest), nil
}

func TestTheManifestSeparatesPathFromContent(t *testing.T) {
	left, err := manifestOf(fstest.MapFS{"ab": &fstest.MapFile{Data: []byte("c")}})
	assert.Must(t, err == nil, "manifestOf: %v", err)
	right, err := manifestOf(fstest.MapFS{"a": &fstest.MapFile{Data: []byte("bc")}})
	assert.Must(t, err == nil, "manifestOf: %v", err)
	assert.Must(t, !bytes.Equal(left, right), "{\"ab\": \"c\"} and {\"a\": \"bc\"} render the same manifest: %s", left)
}

// **An agent checks its install the way the server computed the
// version.** The whole scheme rests on this one equality: the version
// announced is the SHA-256 of the file that ships, so `shasum -a 256
// SHA256SUMS` on disk answers it.
func TestTheVersionIsTheHashOfTheFileThatShips(t *testing.T) {
	archive, err := Zip()
	assert.Must(t, err == nil, "Zip: %v", err)
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	assert.Must(t, err == nil, "reading the archive back: %v", err)

	var shipped []byte
	for _, entry := range reader.File {
		if entry.Name != ManifestName {
			continue
		}
		rc, err := entry.Open()
		assert.Must(t, err == nil, "opening %s: %v", ManifestName, err)
		shipped, err = io.ReadAll(rc)
		if closeErr := rc.Close(); closeErr != nil {
			t.Fatalf("closing %s: %v", ManifestName, closeErr)
		}
		assert.Must(t, err == nil, "reading %s: %v", ManifestName, err)
	}
	assert.Must(t, len(shipped) > 0, "the archive carries no %s: an installed copy cannot check itself", ManifestName)

	sum := sha256.Sum256(shipped)
	assert.Must(t, Version() == "sha256:"+hex.EncodeToString(sum[:]),
		"Version() is %s and hashing the shipped %s gives sha256:%s", Version(), ManifestName, hex.EncodeToString(sum[:]))

	// Every page is listed, and the manifest does not list itself.
	lines := strings.Split(strings.TrimSpace(string(shipped)), "\n")
	listed := map[string]bool{}
	for _, line := range lines {
		parts := strings.SplitN(line, "  ", 2)
		assert.Must(t, len(parts) == 2, "a manifest line is not `<hex>  <path>`: %q", line)
		listed[parts[1]] = true
	}
	assert.Must(t, !listed[ManifestName], "%s lists itself, which no file can honestly do", ManifestName)
	for path := range treeOf(t, Files()) {
		assert.Should(t, listed[path], "%s ships and %s does not list it", path, ManifestName)
	}
}

// TestTheVersionMovesWhenOneByteMoves drives the real tree, not a
// fixture, so a hash that somehow read something other than the bundle
// would be caught here.
func TestTheVersionMovesWhenOneByteMoves(t *testing.T) {
	before, err := versionOfTree(Files())
	assert.Must(t, err == nil, "hashTree: %v", err)
	assert.Must(t, before == Version(), "Version() is %s and hashing the tree gives %s: Version does not hash what ships", Version(), before)
	after, err := versionOfTree(mutated(t, "skill.md", func(body []byte) []byte { return append(body, '.') }))
	assert.Must(t, err == nil, "hashTree: %v", err)
	assert.Must(t, before != after, "appending one byte to skill.md left the version at %s", before)
}

// TestTheVersionMovesWheneverTheZipDoes is the guard on the decision to
// hash the tree rather than the bytes an agent downloads. It partitions
// a set of mutated trees both ways and asserts the partitions agree: no
// two trees may pack to different archives and hash the same, and none
// may pack identically and hash apart.
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
		assert.Must(t, err == nil, "zipTree(%s): %v", name, err)
		sum, err := versionOfTree(tree)
		assert.Must(t, err == nil, "versionOfTree(%s): %v", name, err)
		shapes[name] = shape{zip: string(archive), version: sum}
	}
	for leftName, left := range shapes {
		for rightName, right := range shapes {
			if leftName >= rightName {
				continue
			}
			sameZip := left.zip == right.zip
			sameVersion := left.version == right.version
			assert.Should(t, sameZip == sameVersion, "%q and %q: same archive = %v but same version = %v — the version does not cover what is served",
				leftName, rightName, sameZip, sameVersion)
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
	assert.Must(t, err == nil, "walking the bundle: %v", err)
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
	assert.Must(t, len(out) != 0, "the bundle is empty: every overlay built from it would be empty too")
	return out
}

// mutated returns the bundle with one file rewritten by change, or —
// when change is nil — with that file removed.
func mutated(t *testing.T, path string, change func([]byte) []byte) fstest.MapFS {
	t.Helper()
	out := overlay(t)
	file, ok := out[path]
	assert.Must(t, ok, "%s is not in the bundle: this mutation would be a no-op", path)
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
	assert.Must(t, ok, "%s is not in the bundle: this rename would be a no-op", from)
	delete(out, from)
	out[to] = &fstest.MapFile{Data: file.Data}
	return out
}

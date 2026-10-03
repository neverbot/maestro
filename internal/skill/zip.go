package skill

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"sync"
)

// Zip renders the bundle as a zip archive. Entries are written in sorted
// path order with a zeroed modification time, so the same tree produces
// the same bytes on every build and the same bundle_version on every
// process — an agent's "I already have this one, skip the download" is
// only worth anything if that holds.
func Zip() ([]byte, error) {
	return zipTree(Files())
}

// ManifestName is the file the bundle ships so an installed copy can
// check itself. It is the sha256sum format on purpose: an agent with a
// shell verifies the whole install with one command it already knows.
const ManifestName = "SHA256SUMS"

// Manifest is the SHA256SUMS file as it ships: one line per bundle file,
// in sorted path order. It does not list itself, because a file cannot
// carry its own hash.
func Manifest() ([]byte, error) { return manifestOf(Files()) }

// Version is "sha256:" followed by the hex SHA-256 **of the manifest**.
// Hashing the manifest rather than the tree is what makes the check an
// agent runs the same check the server made: `shasum -a 256 SHA256SUMS`
// against the version it was told, with no knowledge of how this
// repository walks a tree. Any file that changes changes its line in the
// manifest, so the property the tree hash had is kept.
func Version() string {
	versionOnce.Do(func() {
		manifest, err := Manifest()
		if err != nil {
			// Unreachable for the embedded tree: fs.WalkDir over an
			// embed.FS cannot fail on IO, and a bundle that is missing
			// altogether has already panicked in Files().
			panic("skill: hashing the embedded bundle failed: " + err.Error())
		}
		version = VersionOf(manifest)
	})
	return version
}

// VersionOf is Version's body for a manifest somebody already holds —
// the installed copy, in a test, or the one just read out of the zip.
func VersionOf(manifest []byte) string {
	sum := sha256.Sum256(manifest)
	return "sha256:" + hex.EncodeToString(sum[:])
}

var (
	versionOnce sync.Once
	version     string
)

// bundlePaths is every file in a bundle tree, in sorted order. Both the
// zip and the hash walk through it, so they cannot disagree about which
// files the bundle contains: a file one of them can see is a file the
// other sees too.
func bundlePaths(fsys fs.FS) ([]string, error) {
	var paths []string
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

// zipTree is Zip's body, taking the tree as an argument so a test can
// pack a hand-built fs.FS and compare it with the real one. The archive
// carries the manifest beside the pages: an installed copy that cannot
// check itself has to trust a note the installer wrote.
func zipTree(fsys fs.FS) ([]byte, error) {
	paths, err := bundlePaths(fsys)
	if err != nil {
		return nil, fmt.Errorf("skill: walking the bundle: %w", err)
	}
	manifest, err := manifestOf(fsys)
	if err != nil {
		return nil, err
	}
	bodies := map[string][]byte{ManifestName: manifest}
	paths = append(paths, ManifestName)
	sort.Strings(paths)

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, path := range paths {
		body, ok := bodies[path]
		if !ok {
			body, err = fs.ReadFile(fsys, path)
			if err != nil {
				return nil, fmt.Errorf("skill: reading %s: %w", path, err)
			}
		}
		// The header is written by hand rather than through Create so
		// that Modified stays the zero time. zip.Writer.Create stamps
		// time.Now(), which would make every build's archive — and so
		// every ETag over it — different from the last for reasons that
		// have nothing to do with the bundle's content.
		header := &zip.FileHeader{Name: path, Method: zip.Deflate}
		entry, err := w.CreateHeader(header)
		if err != nil {
			return nil, fmt.Errorf("skill: creating the entry for %s: %w", path, err)
		}
		if _, err := entry.Write(body); err != nil {
			return nil, fmt.Errorf("skill: writing %s: %w", path, err)
		}
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("skill: closing the archive: %w", err)
	}
	return buf.Bytes(), nil
}

// manifestOf renders one tree's SHA256SUMS: `<hex>  <path>` per line,
// in sorted path order, which is what `sha256sum -c` reads and what
// `shasum -a 256 SHA256SUMS` hashes on the other side.
func manifestOf(fsys fs.FS) ([]byte, error) {
	paths, err := bundlePaths(fsys)
	if err != nil {
		return nil, fmt.Errorf("skill: walking the bundle: %w", err)
	}
	var out bytes.Buffer
	for _, path := range paths {
		body, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil, fmt.Errorf("skill: reading %s: %w", path, err)
		}
		sum := sha256.Sum256(body)
		fmt.Fprintf(&out, "%s  %s\n", hex.EncodeToString(sum[:]), path)
	}
	return out.Bytes(), nil
}

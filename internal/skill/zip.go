package skill

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
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

// Version is the bundle's content hash: "sha256:" followed by hex.
//
// It hashes the *tree*, not the zip, and it length-prefixes every path
// and every body before hashing them. Without the prefixes, a file
// "ab" holding "c" and a file "a" holding "bc" hash identically, which
// is the classic concatenation collision and is why Nottario's hash is
// built the same way. Computed once, lazily, under a sync.Once: the
// tree cannot change while the process runs.
//
// Hashing the tree rather than the zip bytes is only honest because the
// zip is a pure, deterministic function of the tree and holds exactly
// it: zipTree writes every path the walk finds, in sorted order, with
// the file's bytes and nothing else derived from the host. Two tests pin
// that equivalence rather than leaving it as a claim —
// TestTheZipHoldsExactlyTheTree reads the archive back and compares it
// entry by entry against the tree, and TestTheVersionMovesWheneverTheZipDoes
// partitions a set of mutated trees by zip bytes and by version and
// asserts the two partitions agree. Without them this hash could go on
// covering a tree whose packing had started dropping, reordering or
// rewriting files, which is a hash over bytes nobody is served.
func Version() string {
	versionOnce.Do(func() {
		sum, err := hashTree(Files())
		if err != nil {
			// Unreachable for the embedded tree: fs.WalkDir over an
			// embed.FS cannot fail on IO, and a bundle that is missing
			// altogether has already panicked in Files().
			panic("skill: hashing the embedded bundle failed: " + err.Error())
		}
		version = sum
	})
	return version
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
// pack a hand-built fs.FS and compare it with the real one.
func zipTree(fsys fs.FS) ([]byte, error) {
	paths, err := bundlePaths(fsys)
	if err != nil {
		return nil, fmt.Errorf("skill: walking the bundle: %w", err)
	}
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, path := range paths {
		body, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil, fmt.Errorf("skill: reading %s: %w", path, err)
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

// hashTree is Version's body, taking the tree as an argument for the
// same reason zipTree does: the length-prefixing property is asserted
// against two hand-built trees that collide without it, which is not
// something the real bundle can demonstrate.
func hashTree(fsys fs.FS) (string, error) {
	paths, err := bundlePaths(fsys)
	if err != nil {
		return "", fmt.Errorf("skill: walking the bundle: %w", err)
	}
	sum := sha256.New()
	var length [8]byte
	write := func(b []byte) {
		binary.BigEndian.PutUint64(length[:], uint64(len(b)))
		sum.Write(length[:])
		sum.Write(b)
	}
	for _, path := range paths {
		body, err := fs.ReadFile(fsys, path)
		if err != nil {
			return "", fmt.Errorf("skill: reading %s: %w", path, err)
		}
		write([]byte(path))
		write(body)
	}
	return "sha256:" + hex.EncodeToString(sum.Sum(nil)), nil
}

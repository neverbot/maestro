package web

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
)

//go:embed static
var staticFS embed.FS

// assets is the embedded static tree rooted at static/.
var assets = func() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return sub
}()

// serveAsset writes one embedded file with the right content type. It
// replaces the placeholder of the same name and signature that lived in
// api_projects.go since Task 12 (that stub's own doc comment said this
// task would replace it).
func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, name string) {
	body, err := fs.ReadFile(assets, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case ".css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case ".js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	default:
		// Nothing under static/ has any other extension today, but a
		// missing Content-Type on a future asset would let the browser
		// sniff one instead — exactly what X-Content-Type-Options:
		// nosniff (securityHeaders, server.go) tells it not to do, which
		// would turn a merely-forgotten case here into a broken asset
		// instead of a merely-generic one.
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	// These three files ship inside the binary itself: a new version of
	// any of them only ever appears behind a new deploy, which restarts
	// the process and invalidates any URL-keyed cache anyway, and there
	// is no content hash in the URL for a longer max-age to key off
	// safely. no-cache (not no-store) still lets the browser keep a
	// local copy and revalidate cheaply rather than refetching the full
	// body on every navigation.
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(body)
}

// staticFileServer serves every embedded asset under /static/ except the
// four HTML shells (login.html, index.html, game.html, document.html).
// Those are only ever reachable through GET /login, GET /{$},
// GET /g/{slug} and GET /g/{slug}/doc (server.go) — each with its own
// dispatch logic, handleRoot's redirect decision in particular — so
// serving them again, unconditionally, under
// /static/ as well would hand every one of those pages a second URL that
// bypasses all of that (a quality review found exactly this: /static/
// index.html, wired up through http.FileServerFS with no filtering,
// skipped handleRoot's single-game shortcut and its redirect-to-login for
// an anonymous caller entirely).
func (s *Server) staticFileServer() http.Handler {
	fileServer := http.FileServerFS(assets)
	return http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".html") {
			http.NotFound(w, r)
			return
		}
		fileServer.ServeHTTP(w, r)
	}))
}

// --- The import map and the policy that would otherwise block it ------

// importMapScript finds an inline `<script type="importmap">` and
// captures exactly the bytes between its tags, which is what a
// Content-Security-Policy hash source is computed over.
var importMapScript = regexp.MustCompile(`(?s)<script type="importmap">(.*?)</script>`)

// importMapHashes is one `'sha256-…'` source per distinct import map in
// the shipped shells, sorted, ready to be joined into a script-src.
//
// **This exists because `default-src 'self'` silently switches the
// import map off.** The map is an *inline* script, so a policy with no
// hash, nonce or 'unsafe-inline' makes a browser refuse to apply it —
// and refusing to apply an import map is not a visible failure: the
// element is still in the DOM, `document.querySelector` still finds it,
// and the only symptom is that every bare specifier fails to resolve the
// moment some page imports one. Task 2 shipped the map, Task 15 is the
// first page that imports `lit`, and this is what opening that page in a
// browser found. Nothing before it could have: no test in this package
// enforces a CSP by parsing it, and the map's own tests read the file
// rather than the policy.
//
// A **hash** rather than a nonce or 'unsafe-inline'. 'unsafe-inline'
// would re-admit every injected script this policy exists to refuse. A
// nonce would have to be generated per response and threaded into the
// shell, which means the shells stop being static files. The map is
// shipped in the binary and changes only with a deploy, so its hash is
// computable once, at startup, from the bytes that are actually served
// — which is the property that matters: a map edited without this being
// updated cannot happen, because there is nothing to update.
var importMapHashes = func() []string {
	seen := map[string]bool{}
	entries, err := fs.ReadDir(assets, ".")
	if err != nil {
		panic(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".html") {
			continue
		}
		body, err := fs.ReadFile(assets, entry.Name())
		if err != nil {
			panic(err)
		}
		for _, found := range importMapScript.FindAllSubmatch(body, -1) {
			sum := sha256.Sum256(found[1])
			seen["'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'"] = true
		}
	}
	out := make([]string, 0, len(seen))
	for hash := range seen {
		out = append(out, hash)
	}
	sort.Strings(out)
	return out
}()

// ImportMapHashesForTest is importMapHashes for the external test
// package, which asserts the served policy really admits the map every
// shell ships.
func ImportMapHashesForTest() []string {
	return append([]string(nil), importMapHashes...)
}

// contentSecurityPolicy is the policy every response carries.
//
// script-src repeats 'self' because naming the directive at all replaces
// default-src for scripts: a script-src of hashes alone would refuse
// every module this front end loads.
var contentSecurityPolicy = "default-src 'self'; script-src 'self' " +
	strings.Join(importMapHashes, " ") + "; frame-ancestors 'none'"

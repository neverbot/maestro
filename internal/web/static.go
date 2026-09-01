package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
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
// three HTML shells (login.html, index.html, game.html). Those are only
// ever reachable through GET /login, GET /{$} and GET /g/{slug}
// (server.go) — each with its own dispatch logic, handleRoot's redirect
// decision in particular — so serving them again, unconditionally, under
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

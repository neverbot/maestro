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
	}
	_, _ = w.Write(body)
}

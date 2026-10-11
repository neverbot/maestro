package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The signed URLs this server hands out, and the one place their shape
// is decided.
//
// Three routes use them and none of them is authenticated: the skill
// bundle's download, one image's download, and a document's upload.
// **Possession of a live signature is the authorisation**, which is the
// whole point — what the agent is handed is a URL it will pass to
// something else, usually `curl`, and a URL that only works with a
// header is a URL most of those get wrong.
//
// **The signature covers the arguments and not only the path.** It
// signed the path and an expiry while both routes carried nothing else;
// the upload carries which document, which version it is replacing and
// the hash of the bytes, and a signature that did not cover those would
// admit a URL whose meaning a caller could edit. One canonical message
// now, for all three, so a fourth route cannot invent a fourth format.

// signatureOver returns the hex signature of one canonical message: the
// path, then every argument as `key=value`, sorted by key, each
// separated by a byte no URL component can contain.
//
// The separators are what stop a length confusion — ("/a", "b=c") and
// ("/a\x00b=c", "") would otherwise be one message — and the values are
// the decoded ones, so a signature is over what the arguments *mean*
// rather than over which of several legal spellings was used.
func signatureOver(key []byte, path string, args url.Values) string {
	names := make([]string, 0, len(args))
	for name := range args {
		names = append(names, name)
	}
	sort.Strings(names)

	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(path))
	for _, name := range names {
		mac.Write([]byte{0})
		mac.Write([]byte(name))
		mac.Write([]byte{'='})
		mac.Write([]byte(args.Get(name)))
	}
	mac.Write([]byte{0})
	return hex.EncodeToString(mac.Sum(nil))
}

// signedURL builds a whole URL: base (which may be empty, giving a
// path-only URL the agent resolves against the server it is already
// talking to), the path, the arguments, the expiry and the signature.
func signedURL(key []byte, base, path string, args url.Values, exp time.Time) string {
	signed := url.Values{}
	for name, values := range args {
		if len(values) > 0 {
			signed.Set(name, values[0])
		}
	}
	signed.Set("exp", strconv.FormatInt(exp.Unix(), 10))
	query := signed.Encode()
	signed.Set("sig", signatureOver(key, path, signed))
	return strings.TrimSuffix(base, "/") + path + "?" + query +
		"&sig=" + signed.Get("sig")
}

// signedURLIsLive reports whether this request carries a signature this
// process minted, over these exact arguments, that has not expired.
func (s *Server) signedURLIsLive(r *http.Request) bool {
	args := r.URL.Query()
	sig := args.Get("sig")
	if sig == "" {
		return false
	}
	// **A repeated argument is refused rather than resolved.** Picking
	// the first or the last would be a rule a caller could learn to
	// stand on either side of, and `?v=1&v=2` has no honest canonical
	// form. Nothing this server mints repeats one.
	for name, values := range args {
		if name != "sig" && len(values) > 1 {
			return false
		}
	}
	args.Del("sig")

	exp, err := strconv.ParseInt(args.Get("exp"), 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	want := signatureOver(s.downloadURLKey, r.URL.Path, args)
	// Constant-time, over the hex text rather than the decoded bytes: a
	// malformed hex signature is unequal either way, and this arm never
	// has to decide what a decode error means.
	return hmac.Equal([]byte(want), []byte(sig))
}

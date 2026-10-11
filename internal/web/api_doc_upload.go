package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
)

// A document's bytes, over one unauthenticated signed URL, so prose
// does not travel through the agent's context on the way out.
//
// **This is what the ceiling was never about.** `docs.write` takes the
// markdown as a tool argument and refuses more than a megabyte of it,
// which is a generous bound; the cost that matters is the other one. A
// hundred kilobytes of lore is some thirty thousand tokens the agent has
// to *emit*, and thirty thousand more if it read the document before
// rewriting it. An agent that can write a file and curl it pays neither.
//
// **The signature is the credential and it covers the whole request.**
// Which document, which version is being replaced, and the hash of the
// bytes are all signed, so a URL that leaks is a URL that can write
// those exact bytes onto that exact version and nothing else. The hash
// is what makes that true: without it the URL would admit any content
// at all.
//
// **Single use, without anything remembering it.** The signed version
// is the one being replaced, so the write that lands moves the document
// past it and a second upload of the same URL answers version_conflict.
// Nothing stores a used ticket; the conflict rule the product already
// has does the work.
const docUploadPath = "/upload/doc/"

// uploadURLTTL is how long a signed upload URL stays good. Five
// minutes: an agent mints one and sends the file immediately, which is
// the whole shape of the thing, and a URL that has leaked into a
// transcript is worthless by the time anybody reads it. The image
// download's hour is longer because it is read out of an answer and
// acted on later; this is not.
const uploadURLTTL = 5 * time.Minute

// docUploadTicket is everything the minting call fixes about an upload.
// It is the argument list of the eventual write, signed.
type docUploadTicket struct {
	Path            string
	ExpectedVersion int32
	SHA256          string
	Kind            string
	Message         string
	// The author the version will carry. **Signed, not sent**: the
	// upload has no caller to attribute, so the actor is fixed when the
	// URL is minted and cannot be edited afterwards.
	UserID  uuid.UUID
	TokenID *uuid.UUID
}

func (t docUploadTicket) args() url.Values {
	args := url.Values{}
	args.Set("path", t.Path)
	args.Set("version", strconv.FormatInt(int64(t.ExpectedVersion), 10))
	args.Set("sha256", t.SHA256)
	args.Set("user", t.UserID.String())
	if t.Kind != "" {
		args.Set("kind", t.Kind)
	}
	if t.Message != "" {
		args.Set("message", t.Message)
	}
	if t.TokenID != nil {
		args.Set("token", t.TokenID.String())
	}
	return args
}

// signedDocUploadURL is the whole upload URL for one document.
func signedDocUploadURL(key []byte, base string, projectID uuid.UUID, ticket docUploadTicket, exp time.Time) string {
	return signedURL(key, base, docUploadPath+projectID.String(), ticket.args(), exp)
}

// handleDocUpload writes one document from the request body.
func (s *Server) handleDocUpload(w http.ResponseWriter, r *http.Request) {
	if !s.signedURLIsLive(r) {
		// 401 and no body, as the other two signed routes answer: there
		// is nothing a caller can do with a description of why a
		// signature failed that they cannot do by minting a fresh URL.
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if s.opts.Markdown == nil {
		http.NotFound(w, r)
		return
	}
	projectID, err := uuid.Parse(r.PathValue("project"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	args := r.URL.Query()
	version, err := strconv.ParseInt(args.Get("version"), 10, 32)
	if err != nil {
		writeError(w, http.StatusBadRequest, errCodeInvalidInput, "version: not a number")
		return
	}

	// **One byte over the bound is refused as the argument's problem,
	// not as a broken request.** MaxBytesReader stops the read, and the
	// extra byte is what tells a full body from an over-full one.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(markdown.MaxBodyBytes)+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, errCodeInvalidInput,
			"content: the upload ended before the body did")
		return
	}
	if len(body) > markdown.MaxBodyBytes {
		writeError(w, http.StatusBadRequest, errCodeInvalidInput,
			"content: holds at most "+strconv.Itoa(markdown.MaxBodyBytes)+" bytes")
		return
	}

	// **The hash is checked before anything is written.** The URL was
	// signed over it, so this is both an integrity check on the transfer
	// and the thing that stops a leaked URL writing content nobody
	// intended.
	sum := sha256.Sum256(body)
	got := hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(got), []byte(args.Get("sha256"))) != 1 {
		writeError(w, http.StatusBadRequest, errCodeInvalidInput,
			"content: these bytes are not the ones this URL was signed for, and nothing was written")
		return
	}

	expected := int32(version)
	write := markdown.WriteInput{
		Path:            args.Get("path"),
		Content:         string(body),
		Message:         args.Get("message"),
		ExpectedVersion: &expected,
		Actor:           uploadActor(args),
	}
	if kind := args.Get("kind"); kind != "" {
		write.Kind = &kind
	}
	// The same core `docs.write` calls, so the bounds, the frontmatter
	// parsing, the derived title and the conflict answer are one
	// implementation and not two.
	row, err := s.opts.Markdown.Write(r.Context(), projectID, write)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, DocUploadedOutput{
		Path: row.Path, Kind: row.Kind, Title: row.Title,
		Version: row.CurrentVersion, UpdatedAt: row.UpdatedAt.Time.UTC().Format(time.RFC3339),
	})
}

// DocUploadedOutput is the whole answer to an upload.
//
// **The body is not echoed back.** The caller just sent it, and sending
// it back is the cost this route exists to avoid, paid twice.
type DocUploadedOutput struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Version   int32  `json:"version"`
	UpdatedAt string `json:"updated_at"`
}

// uploadActor rebuilds the author from the signed arguments.
func uploadActor(args url.Values) metamodel.Actor {
	actor := metamodel.Actor{}
	if id, err := uuid.Parse(args.Get("user")); err == nil {
		actor.UserID = &id
	}
	if id, err := uuid.Parse(args.Get("token")); err == nil {
		actor.TokenID = &id
	}
	return actor
}

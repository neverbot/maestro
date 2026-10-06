package web

import (
	"net/http"

	"github.com/google/uuid"
)

// The REST mirror of the three comment tools. Route for route, as every
// tool on this surface is mirrored.
//
// **The listing is a GET and takes its target in the query string**,
// because a read is a read: a page asking for one thing's log should not
// have to post a body to get it.

func (s *Server) requireCommentService(w http.ResponseWriter) bool {
	if s.opts.Comments == nil {
		writeError(w, http.StatusNotFound, errCodeNotFound, "this instance keeps no comment log")
		return false
	}
	return true
}

// targetFromQuery reads the four shapes out of a query string. An absent
// `on` is no target at all, which is how the game's whole log is asked
// for.
func targetFromQuery(r *http.Request) *CommentTargetInput {
	on := r.URL.Query().Get("on")
	if on == "" {
		return nil
	}
	q := r.URL.Query()
	target := &CommentTargetInput{On: on, TypeKey: q.Get("type_key"), Key: q.Get("key")}
	if source := q.Get("source_key"); source != "" {
		target.Source = &RefInput{TypeKey: q.Get("source_type"), Key: source}
	}
	if to := q.Get("target_key"); to != "" {
		target.Target = &RefInput{TypeKey: q.Get("target_type"), Key: to}
	}
	return target
}

func (s *Server) handleListComments(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireCommentService(w) {
		return
	}
	limit, ok := queryLimit(w, r)
	if !ok {
		return
	}
	out, err := commentsList(r.Context(), s.deps(), caller, scope.ProjectID,
		CommentsListInput{Target: targetFromQuery(r), Limit: limit})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAddComment(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireCommentService(w) {
		return
	}
	var in CommentsAddInput
	if !decodeContentBody(w, r, &in) || !checkStatedProject(w, scope, in) {
		return
	}
	out, err := commentsAdd(r.Context(), s.deps(), caller, scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRemoveComment(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireCommentService(w) {
		return
	}
	id, err := uuid.Parse(r.PathValue("comment"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errCodeInvalidInput, "comment: not an id")
		return
	}
	out, removeErr := commentsRemove(r.Context(), s.deps(), caller, scope.ProjectID, CommentsRemoveInput{ID: id})
	if removeErr != nil {
		s.writeDomainError(w, r, removeErr)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

package web

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/markdown"
)

// The REST mirror of the three comment tools. Route for route, as every
// tool on this surface is mirrored.
//
// **The listing is a GET and takes its target in the query string**,
// because a read is a read: a page asking for one thing's log should not
// have to post a body to get it.

// CommentReadOutput is one entry as a browser reads it: the answer every
// surface gets, plus the body rendered.
//
// **Rendering is a REST-only affordance**, the rule internal/markdown's
// header states and api_entity_prose.go applies to a field value: an
// agent asked to rewrite something needs the markdown, so comments.list
// answers with CommentOutput and nothing more.
type CommentReadOutput struct {
	CommentOutput
	BodyHTML string `json:"body_html"`
}

// CommentsReadOutput is a log as a browser reads it.
type CommentsReadOutput struct {
	Items []CommentReadOutput `json:"items"`
}

func readable(items []CommentOutput) (CommentsReadOutput, error) {
	out := CommentsReadOutput{Items: make([]CommentReadOutput, 0, len(items))}
	for _, item := range items {
		body, err := markdown.RenderField(item.Body)
		if err != nil {
			return CommentsReadOutput{}, err
		}
		out.Items = append(out.Items, CommentReadOutput{CommentOutput: item, BodyHTML: body})
	}
	return out, nil
}

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
	return &CommentTargetInput{On: on, TypeKey: q.Get("type_key"), Key: q.Get("key")}
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
	read, err := readable(out.Items)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, read)
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
	read, err := readable([]CommentOutput{out})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, read.Items[0])
}

func (s *Server) handleRemoveComment(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireCommentService(w) {
		return
	}
	// The path segment is checked here so the refusal can name *it*, and
	// handed on as text because that is what the argument is on both
	// surfaces now.
	id, err := uuid.Parse(r.PathValue("comment"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errCodeInvalidInput, "comment: not an id")
		return
	}
	out, removeErr := commentsRemove(r.Context(), s.deps(), caller, scope.ProjectID, CommentsRemoveInput{ID: id.String()})
	if removeErr != nil {
		s.writeDomainError(w, r, removeErr)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

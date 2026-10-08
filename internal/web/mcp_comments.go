package web

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/comments"
	"github.com/neverbot/maestro/internal/metamodel"
)

// The comment tools. A comment is markdown about the work — how a thing
// was imported, what was rewritten and why, an idea worth keeping — and
// it is addressed the way everything else on this surface is addressed:
// by keys, never by ids.
//
// **The target is one of four and the arguments say which**, rather than
// a (kind, id) pair: a tool whose arguments depend on a string argument
// is a tool an agent gets wrong once per session.

// CommentTargetInput is the thing a comment is about: an entity
// (type_key and key), an entity type or a relation type (type_key
// alone), said by `on`.
type CommentTargetInput struct {
	On      string `json:"on"`
	TypeKey string `json:"type_key"`
	Key     string `json:"key,omitempty"`
}

// CommentsAddInput is the argument shape of comments.add.
type CommentsAddInput struct {
	ScopedArgs
	Target CommentTargetInput `json:"target"`
	Body   string             `json:"body"`
}

// CommentsListInput is the argument shape of comments.list. With no
// target it reads the game's whole log.
type CommentsListInput struct {
	ScopedArgs
	Target *CommentTargetInput `json:"target,omitempty"`
	Limit  int32               `json:"limit,omitempty"`
}

// CommentsRemoveInput is the argument shape of comments.remove.
type CommentsRemoveInput struct {
	ScopedArgs
	// **Text, not a uuid.UUID.** The MCP SDK builds a tool's schema by
	// reflecting this struct, and a uuid.UUID is a [16]byte: the tool
	// went out declaring id as an array of sixteen numbers, which the
	// server then refused because its own decoder wants the text. An
	// agent could satisfy the schema or the server and never both.
	ID string `json:"id"`
}

// commentID reads the one id this surface takes as an argument.
func commentID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return uuid.Nil, &metamodel.ValidationError{
			Code:   metamodel.CodeInvalidInput,
			Fields: []metamodel.FieldError{{Path: "id", Message: "must be the text of a UUID, as comments.list answers with"}},
		}
	}
	return id, nil
}

// CommentOutput is one comment.
type CommentOutput struct {
	ID        uuid.UUID `json:"id"`
	On        string    `json:"on"`
	Body      string    `json:"body"`
	CreatedAt string    `json:"created_at"`
	// Author is who wrote it: a display name for a person, the token's
	// label for an agent, and empty for neither.
	Author string `json:"author,omitempty"`
	// ByAgent says which of the two that was, because the name alone
	// cannot: a token's label is a word somebody chose, and a reader who
	// cannot tell a machine from a colleague is reading a log that is
	// lying to them by omission.
	ByAgent bool `json:"by_agent"`
	// AuthorOf is the person an agent's token belongs to, and the author
	// themselves when a person wrote it.
	AuthorOf string `json:"author_of,omitempty"`
}

// CommentsListOutput is a log, newest first.
type CommentsListOutput struct {
	Items []CommentOutput `json:"items"`
}

// CommentRemovedOutput says the comment is gone.
type CommentRemovedOutput struct {
	ID      uuid.UUID `json:"id"`
	Removed bool      `json:"removed"`
}

func targetOf(in CommentTargetInput) (comments.Target, error) {
	kind := comments.Kind(in.On)
	switch kind {
	case comments.OnEntity, comments.OnEntityType, comments.OnRelationType:
	default:
		return comments.Target{}, &metamodel.ValidationError{
			Code: metamodel.CodeInvalidInput,
			Fields: []metamodel.FieldError{{Path: "target.on",
				Message: fmt.Sprintf("must be %q, %q or %q", comments.OnEntity,
					comments.OnEntityType, comments.OnRelationType)}},
		}
	}
	return comments.Target{Kind: kind, TypeKey: in.TypeKey, Key: in.Key}, nil
}

// commentOf is one entry on the wire. The time is RFC 3339, as every
// other timestamp this surface answers with.
func commentOf(row comments.Comment) CommentOutput {
	return CommentOutput{
		ID:        row.ID,
		On:        string(row.Kind),
		Body:      row.Body,
		CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339),
		Author:    row.Author,
		ByAgent:   row.ByAgent,
		AuthorOf:  row.AuthorOf,
	}
}

// MCPCommentsAdd implements comments.add.
func MCPCommentsAdd(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in CommentsAddInput) (CommentOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return CommentOutput{}, err
	}
	return commentsAdd(ctx, deps, caller, projectID, in)
}

// commentsAdd is MCPCommentsAdd without the token-binding check, for the
// REST mirror. See mcp_metamodel.go's header.
func commentsAdd(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in CommentsAddInput) (CommentOutput, error) {
	target, err := targetOf(in.Target)
	if err != nil {
		return CommentOutput{}, err
	}
	row, err := deps.Comments.Add(ctx, projectID, target, in.Body, actorOf(caller))
	if err != nil {
		return CommentOutput{}, err
	}
	return commentOf(row), nil
}

// MCPCommentsList implements comments.list.
func MCPCommentsList(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in CommentsListInput) (CommentsListOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return CommentsListOutput{}, err
	}
	return commentsList(ctx, deps, caller, projectID, in)
}

func commentsList(ctx context.Context, deps MCPDeps, _ Caller, projectID uuid.UUID, in CommentsListInput) (CommentsListOutput, error) {
	var (
		rows []comments.Comment
		err  error
	)
	if in.Target == nil {
		rows, err = deps.Comments.ListGame(ctx, projectID, in.Limit)
	} else {
		var target comments.Target
		target, err = targetOf(*in.Target)
		if err != nil {
			return CommentsListOutput{}, err
		}
		rows, err = deps.Comments.List(ctx, projectID, target, in.Limit)
	}
	if err != nil {
		return CommentsListOutput{}, err
	}
	items := make([]CommentOutput, 0, len(rows))
	for _, row := range rows {
		items = append(items, commentOf(row))
	}
	return CommentsListOutput{Items: items}, nil
}

// MCPCommentsRemove implements comments.remove.
func MCPCommentsRemove(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in CommentsRemoveInput) (CommentRemovedOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return CommentRemovedOutput{}, err
	}
	return commentsRemove(ctx, deps, caller, projectID, in)
}

func commentsRemove(ctx context.Context, deps MCPDeps, _ Caller, projectID uuid.UUID, in CommentsRemoveInput) (CommentRemovedOutput, error) {
	id, err := commentID(in.ID)
	if err != nil {
		return CommentRemovedOutput{}, err
	}
	if err := deps.Comments.Remove(ctx, projectID, id); err != nil {
		return CommentRemovedOutput{}, err
	}
	return CommentRemovedOutput{ID: id, Removed: true}, nil
}

// addCommentTools registers the three.
func (s *Server) addCommentTools(srv *mcp.Server, deps MCPDeps) {
	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "comments.add",
		Description: fmt.Sprintf("Write one comment in a game's log. **A comment is markdown about the "+
			"work, not about the game**: how a thing was imported, what was rewritten and why, an "+
			"idea about the philosophy of a type, something worth doing later. What a player can "+
			"be, go to, do or unlock is a field or a document; nothing here is read by a query, a "+
			"view or an analysis. "+
			"**A person reads this.** Every comment is drawn on the thing's own page, under its "+
			"content, newest first and with who wrote it; the game's designers write there too, in "+
			"the same band, and what they write comes back from comments.list. Write for that "+
			"reader: \"imported from the old engine's data files, the damage formula is a guess "+
			"from two log lines\" is worth opening a page for, and \"updated\" is not. "+
			"target.on is %q, %q or %q, and the rest of target is the address that kind takes: "+
			"an entity is type_key plus key and either type is type_key alone. An address this "+
			"game does not have is not_found rather than a comment nothing can ever read. "+
			"**One relation cannot carry one**, deliberately: an edge has no page of its own, so "+
			"a note left there would be reachable by comments.list and by nothing a designer "+
			"opens. A fact a player could meet is a field on the edge, where a query, a view and "+
			"relations.repair can all reach it; a note about how a kind of connection changed goes "+
			"on the relation type or on one of the entities. "+
			"body is markdown and holds at most %d characters — prose that wants a title, a "+
			"history and an address of its own is a document. "+
			"**There is no state on a comment and there will not be one**: no status, no assignee, "+
			"no due date, no done. It is a log of what was thought, and the day it carries a state "+
			"it has become a project tracker, which this product is not. "+
			"**There is no edit, either.** A log that can be rewritten is not a log; comments.remove "+
			"takes one out whole.",
			comments.OnEntity, comments.OnEntityType, comments.OnRelationType,
			comments.MaxBodyRunes),
		OutputSchema: commentOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in CommentsAddInput) (CommentOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPCommentsAdd(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "comments.list",
		Description: fmt.Sprintf("Read a log, newest first. With a target it is that one thing's log; "+
			"**with no target at all it is the game's**, which is what has been thought about this "+
			"game lately whatever each note was about — the call to make when you arrive and want "+
			"to know what happened since you were last here. limit defaults to %d and is capped at "+
			"%d. The log is not paged beyond that: it is a log, and the whole of a thing's is the "+
			"size of what somebody wrote.",
			comments.DefaultPage, comments.MaxPage),
		OutputSchema: commentsListOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in CommentsListInput) (CommentsListOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPCommentsList(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "comments.remove",
		Description: "Take one comment out of the log, by the id comments.add and comments.list " +
			"answer with. It is the only way to change a log: there is no edit, because a note is " +
			"what somebody said at a moment. An id this game's log does not carry is not_found.",
		OutputSchema: commentRemovedOutputSchema,
		Annotations:  &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: boolPtr(true)},
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in CommentsRemoveInput) (CommentRemovedOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPCommentsRemove(ctx, deps, caller, projectID, in)
	})
}

// The three answers, hand-written for the reason every schema on this
// surface is: the SDK validates against the marshalled JSON, and a shape
// inferred by reflection is a shape nobody read.
var commentOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"id", "on", "body", "created_at", "by_agent"},
	Properties: map[string]*jsonschema.Schema{
		"id":         stringSchema(),
		"on":         stringSchema(),
		"body":       stringSchema(),
		"created_at": stringSchema(),
		"author":     stringSchema(),
		"by_agent":   boolSchema(),
		"author_of":  stringSchema(),
	},
}

var commentsListOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"items"},
	Properties: map[string]*jsonschema.Schema{
		"items": {Type: "array", Items: commentOutputSchema},
	},
}

var commentRemovedOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"id", "removed"},
	Properties: map[string]*jsonschema.Schema{
		"id":      stringSchema(),
		"removed": boolSchema(),
	},
}

package web

import (
	"context"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
)

// EntityReadOutput is the entity a browser reads: the answer every
// surface gets, plus its longtext values rendered to HTML.
//
// **Rendering is a REST-only affordance**, the rule internal/markdown's
// header states for a document body and this applies to a field value:
// an agent asked to rewrite a field needs the markdown it will edit, so
// MCP's entities.get answers with EntityOutput and nothing more. The
// extra key is on the embedded struct's own level, which is what lets a
// client read one entity shape on both routes.
type EntityReadOutput struct {
	EntityOutput

	// FieldsHTML holds one entry per longtext field the row has a value
	// for, keyed the way `fields` is. It is absent when the type declares
	// no longtext, so a client that finds no entry for a key renders the
	// value as text rather than deciding by type a second time.
	FieldsHTML map[string]string `json:"fields_html,omitempty"`
}

// entityRead is entitiesGet plus the rendering. The type is read for its
// schema, because which of a row's values is prose is a fact about the
// type and never about the value.
func entityRead(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in EntitiesGetInput) (EntityReadOutput, error) {
	entity, err := entitiesGet(ctx, deps, caller, projectID, in)
	if err != nil {
		return EntityReadOutput{}, err
	}
	typ, err := typesGet(ctx, deps, caller, projectID, TypesGetInput{Key: entity.TypeKey})
	if err != nil {
		return EntityReadOutput{}, err
	}
	rendered := map[string]string{}
	for _, field := range typ.Schema {
		if field.Type != metamodel.FieldLongText {
			continue
		}
		value, ok := entity.Fields[field.Key].(string)
		if !ok || value == "" {
			continue
		}
		html, err := markdown.RenderField(value)
		if err != nil {
			return EntityReadOutput{}, err
		}
		rendered[field.Key] = html
	}
	if len(rendered) == 0 {
		return EntityReadOutput{EntityOutput: entity}, nil
	}
	return EntityReadOutput{EntityOutput: entity, FieldsHTML: rendered}, nil
}

package web

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/assert"
)

// **Every argument on this surface is something an agent can type.**
// `comments.remove` declared its id as a `uuid.UUID`, which is a
// `[16]byte`, and the SDK reflects a byte array into a schema of sixteen
// numbers. An agent that sent the sixteen got `cannot unmarshal array
// ... of type *uuid.UUID` back from the server, and one that sent the
// text of the UUID had the call refused by its own client before it left
// — `has type "string", want "array"`. The tool was unusable in both
// directions and nothing was red, because every test handed the function
// a uuid.UUID it already had instead of going through the schema the SDK
// publishes. Correct in the module, dead at the call site.
//
// A comment is the one thing here with no key of its own, so it is the
// one place an id is an argument at all. The schema only exists on the
// wire, so this reads it from there.
func TestEveryToolArgumentIsSomethingAnAgentCanType(t *testing.T) {
	t.Parallel()
	_, session := everyToolOverTheWire(t)

	tools, err := session.ListTools(context.Background(), nil)
	assert.Must(t, err == nil, "ListTools: %v", err)
	assert.Must(t, len(tools.Tools) > 20, "tools/list returned %d tools; this guard is reading the wrong build", len(tools.Tools))

	seen := 0
	for _, tool := range tools.Tools {
		if tool.InputSchema == nil {
			continue
		}
		raw, err := json.Marshal(tool.InputSchema)
		assert.Must(t, err == nil, "marshal %s's input schema: %v", tool.Name, err)
		// A property is itself a schema, and a schema may be the bare
		// `true` that means "anything goes", so the properties are read
		// one raw value at a time rather than into a shape that assumes
		// every one is an object.
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		assert.Must(t, json.Unmarshal(raw, &schema) == nil, "read %s's input schema: %s", tool.Name, raw)
		for name, prop := range schema.Properties {
			if name != "id" && !strings.HasSuffix(name, "_id") {
				continue
			}
			var typed struct {
				// A nullable argument is typed `["null","string"]`
				// rather than `"string"`, so this is read raw and the
				// null taken out before the comparison.
				Type json.RawMessage `json:"type"`
			}
			if json.Unmarshal(prop, &typed) != nil || len(typed.Type) == 0 {
				continue
			}
			seen++
			assert.Should(t, jsonType(t, typed.Type) == "string", "%s declares %s as %s rather than a string: an "+
				"id reaches this surface as the text of a UUID, and a schema saying otherwise is a tool nothing "+
				"can call", tool.Name, name, typed.Type)
		}
	}
	assert.Should(t, seen > 0, "no tool declares an id argument at all: this guard is asserting nothing")
}

// **An argument published with no type at all is an argument a client
// guesses at, and it guesses wrong.** views.validate, views.upsert and
// views.run declared their query as the empty schema, which marshals to
// `true`: anything goes. A client with nothing to go on sends the
// object the agent wrote as a *string*, the server refuses the string,
// and the three tools cannot be called at all — which is how they
// shipped, and how they stayed, because nothing was wrong with the
// handlers and an object had never reached one.
//
// **This guard existed and stepped over it.** The one above skips a
// property whose schema carries no type, with a comment naming the
// query as the reason; it was written to be about ids and it saw this
// on the way past. Reading a hole and moving on is how a hole lasts.
func TestNoToolArgumentIsPublishedWithoutAType(t *testing.T) {
	t.Parallel()
	_, session := everyToolOverTheWire(t)

	tools, err := session.ListTools(context.Background(), nil)
	assert.Must(t, err == nil, "ListTools: %v", err)
	assert.Must(t, len(tools.Tools) > 20, "tools/list returned %d tools; this guard is reading the wrong build", len(tools.Tools))

	checked := 0
	for _, tool := range tools.Tools {
		if tool.InputSchema == nil {
			continue
		}
		raw, err := json.Marshal(tool.InputSchema)
		assert.Must(t, err == nil, "marshal %s's input schema: %v", tool.Name, err)
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		assert.Must(t, json.Unmarshal(raw, &schema) == nil, "read %s's input schema: %s", tool.Name, raw)
		for name, prop := range schema.Properties {
			checked++
			// `true` and `{}` are the same schema and both mean
			// "anything": a property that is either is one no client can
			// serialise correctly except by luck.
			flat := strings.Join(strings.Fields(string(prop)), "")
			assert.Should(t, flat != "true" && flat != "{}",
				"%s publishes %s as %s, which admits anything and tells a client nothing: it will send a "+
					"string where an object is meant", tool.Name, name, flat)

			var typed struct {
				Type json.RawMessage `json:"type"`
			}
			assert.Must(t, json.Unmarshal(prop, &typed) == nil, "read %s's %s: %s", tool.Name, name, prop)
			assert.Should(t, len(typed.Type) > 0,
				"%s publishes %s with no type: %s", tool.Name, name, prop)
		}
	}
	assert.Must(t, checked > 60, "checked %d arguments; this guard is reading the wrong place", checked)
}

// jsonType reads a schema's `type`, which is one name or a list of them,
// and answers the one that is not "null".
func jsonType(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one
	}
	var many []string
	assert.Must(t, json.Unmarshal(raw, &many) == nil, "a schema type that is neither a name nor a list: %s", raw)
	for _, name := range many {
		if name != "null" {
			return name
		}
	}
	return "null"
}

// **The three tools that take a query, driven the way a client drives
// them.** They were unusable from an MCP client and every test of them
// passed, because every test called the Go function with bytes already
// in hand: the schema a client reads was never in the picture, and the
// schema was the whole defect. This goes through tools/call.
func TestTheQueryToolsTakeAnObjectOverTheWire(t *testing.T) {
	t.Parallel()
	_, session := everyToolOverTheWire(t)
	ctx := context.Background()

	// A query this game can answer: the type is declared below, so the
	// only thing being judged is whether the argument arrived at all.
	_, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "types.upsert",
		Arguments: map[string]any{"key": "quest", "label": "Quest", "label_plural": "Quests"},
	})
	assert.Must(t, err == nil, "declare a type: %v", err)

	query := map[string]any{"v": 1, "from": []any{map[string]any{"type": "quest"}}}

	for _, tool := range []struct {
		name string
		args map[string]any
	}{
		{"views.validate", map[string]any{"query": query}},
		{"views.run", map[string]any{"query": query}},
		{"views.upsert", map[string]any{
			"key": "all-quests", "name": "All quests", "query": query,
			"renderer": "table", "expected_version": 0,
		}},
	} {
		t.Run(tool.name+" takes an object", func(t *testing.T) {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool.name, Arguments: tool.args})
			assert.Must(t, err == nil, "CallTool(%s): %v", tool.name, err)
			assert.Should(t, !result.IsError, "%s refused a well-formed query: %+v", tool.name, result.Content)
		})

		t.Run(tool.name+" refuses a string", func(t *testing.T) {
			// **The refusal is the SDK's, from the schema, and that is
			// the design.** The handler never sees it, so there is
			// nothing in the handler to be tolerant with: a tolerance
			// no request can reach is a mechanism nothing reads. What
			// this pins is that the schema is enforced and that the
			// message names the argument.
			asText := map[string]any{}
			for k, v := range tool.args {
				asText[k] = v
			}
			encoded, err := json.Marshal(query)
			assert.Must(t, err == nil, "marshal the query: %v", err)
			asText["query"] = string(encoded)

			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool.name, Arguments: asText})
			assert.Must(t, err == nil, "CallTool(%s): %v", tool.name, err)
			assert.Must(t, result.IsError, "%s accepted a query sent as a string", tool.name)
			said := ""
			for _, part := range result.Content {
				if text, ok := part.(*mcp.TextContent); ok {
					said += text.Text
				}
			}
			assert.Should(t, strings.Contains(said, "query") && strings.Contains(said, "object"),
				"%s refused a stringified query without saying which argument or what it wanted: %q",
				tool.name, said)
		})
	}
}

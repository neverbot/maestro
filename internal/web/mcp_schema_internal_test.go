package web

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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
		// A property is itself a schema, and a schema may be `true` —
		// "anything goes", which views.run's free-form query is — so the
		// properties are read one raw value at a time rather than into a
		// shape that assumes every one is an object.
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

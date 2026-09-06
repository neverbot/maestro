// A renderer's controls, and the one sentence each of them teaches.
//
// Every renderer in the catalogue has knobs — `graph` has six, `map` has
// four — and every one of them is a control in the "Save as" dialog
// (Task 16) with a tooltip beside it. This module is the shape of that
// control, shared, so the six renderers declare their knobs the same way
// and one surface can paint any of them without knowing which renderer
// it is looking at.
//
// **The tooltip is not decoration and it is not the server's sentence.**
// internal/views/renderers.go carries a `Doc` per parameter and says in
// its own header that it "describes a contract, not an appearance": its
// words are what a *query* must produce for a parameter to be legal,
// written for an agent. What a designer needs at the control is what the
// knob does to the picture — and for `graph`'s `group_by` and
// `cluster_by` that is the whole difference between them, since one
// draws an enclosure and the other draws nothing at all. There is
// nowhere else in the interface that difference can be learned, which is
// why these sentences are asserted rather than admired.
//
// So the two are complementary and neither restates the other: the
// catalogue says what a parameter *is*, a tooltip says what it *does to
// the drawing*. A control's tooltip therefore lives with the renderer
// that draws it, and this module is only the shape.

// The kinds a control can be. They mirror internal/views/renderers.go's
// ParamKind vocabulary in the spellings the wire uses, because a control
// that offered a kind the server refuses is a control that composes an
// invalid document.
export const CONTROL_SLOT = "slot";
export const CONTROL_BOOL = "bool";
export const CONTROL_ENUM = "enum";
export const CONTROL_NUMBER = "number";
export const CONTROL_TEXT = "text";
// A whole number of at least 1 — `nested`'s `max_depth`. It is its own
// kind and not `number` because the server's is: a control that offered
// 0 or 2.5 would compose a document views.upsert refuses.
export const CONTROL_COUNT = "count";
// The key of a relation type this game declares — `nested`'s
// `contain_via`, which is required.
export const CONTROL_RELATION_TYPE = "relation_type";
// `layered`'s `rank_by`: the literal "edges", or a declared number field
// key. Neither an enum nor a slot, and the server spells it as its own
// kind for that reason.
export const CONTROL_RANK_BY = "rank_by";
// `map`'s `x_field` and `y_field`: a declared **number field** key the
// query carries in `project.fields`. It is not `number` — that is a
// value, this is the name of a field — and it is not `slot`, because a
// slot is a thing the projection computed and this is a column of the
// entity the game declared.
export const CONTROL_NUMBER_FIELD = "number_field";
// `timeline`'s `axis_field` and `axis_end_field`: a declared number
// **or enum** field key. Its own kind and not number_field's, because an
// enum axis is legal here and nowhere else.
export const CONTROL_AXIS_FIELD = "axis_field";
// `table`'s `sort` and `columns`: one column reference, and a list of
// them in the order they are drawn. Two kinds because the server has
// two: a list is not a value repeated.
export const CONTROL_COLUMN = "column";
export const CONTROL_COLUMNS = "columns";

// control is one knob.
//
//   name    — the renderer_params key, exactly as the server spells it.
//   kind    — one of the above.
//   tooltip — what this knob does to the picture, in one sentence.
//   values  — the admitted spellings, for an enum, and null otherwise.
//
// Frozen, because a control is a description and a surface that could
// edit one would be a surface that could teach a designer something the
// renderer does not do.
export function control(name, kind, tooltip, values = null) {
  return Object.freeze({
    name,
    kind,
    tooltip,
    values: values === null ? null : Object.freeze([...values]),
  });
}

// controlNamed is the lookup, so a test and a dialog find a control the
// same way.
export function controlNamed(controls, name) {
  for (const entry of Array.isArray(controls) ? controls : []) {
    if (entry && entry.name === name) return entry;
  }
  return null;
}

// One entity: its name, its address, its fields, the edges either side
// of it, and the prose attached to it.
//
// **A field the entity does not carry is rendered, not hidden.** *What
// could be filled in here* is the question a designer is usually asking,
// and a page that showed only what happens to be set answers a different
// one — it makes an empty schema and a fully-populated one look the
// same. The row is marked `absent` and wears the em dash, which is
// render/twin.js's own cell shape, called rather than restated: the twin,
// the `table` renderer and this page are three places a reader meets the
// same value, and one function is what makes them agree.
//
// **A relation carries its own fields.** A relation type may declare a
// field schema, `relations.upsert` validates an edge's values against it
// and stores them, and until this page nothing in the interface showed
// them — a whole declared feature that was write-only for a whole
// sub-project. The readme's own example of why typed edges exist, a door
// declaring which ability opens it, is a row on this page now.
//
// **Both directions are two lists and never one.** An edge is directed;
// asking once and sorting the answer would be inventing a direction the
// server did not state, and "what leads here" and "what leads away" are
// two questions a designer asks separately.
//
// The same body is what a node click opens over a canvas
// (`pages/view.js`), so this module exports the builder as well as
// driving the page. **It runs no view**: it reads the entity, its type,
// its edges and its documents, and `theEntityPanelRunsNoViews` pins that
// the panel over a diagram costs the diagram nothing.

import { absentCell, presentCell } from "../render/twin.js";
import {
  countLabel,
  destinations,
  DESTINATION_CATALOGUE,
  docURL,
  entityURL,
  expired,
  fail,
  openGame,
  say,
  segmentsOf,
  typeURL,
} from "./page.js";
import { goToLogin } from "../app.js";

// The metamodel's declared field types, in its own spelling
// (internal/metamodel/schema.go). They are constants because this module
// renders a value *by* its declared type and a mistyped comparison would
// silently fall through to the default arm.
export const FIELD_TEXT = "text";
export const FIELD_LONGTEXT = "longtext";
export const FIELD_NUMBER = "number";
export const FIELD_BOOL = "bool";
export const FIELD_ENUM = "enum";
export const FIELD_LIST_TEXT = "list<text>";

// What a boolean reads as. "yes"/"no" and not "true"/"false": a designer
// declaring `repeatable` is asking a question about the game, not about
// a JSON literal.
export const BOOL_TRUE = "yes";
export const BOOL_FALSE = "no";

// The separator a list of text is joined with.
export const LIST_SEPARATOR = ", ";

// What the two relation lists are called, in the model, so the page and
// the harness name them the same way.
export const DIRECTION_OUT = "out";
export const DIRECTION_IN = "in";

// The line the catalogue page and this one both need: an entity that no
// longer fits its type is kept and marked, never deleted, and this is
// where a designer meets that fact about one row.
export const INVALID_NOTE = "This entity no longer fits its type's field schema.";

// --- The model -------------------------------------------------------

// formatValue renders one value by its declared type.
//
// Every arm answers with a string and none of them answers with a blank:
// a blank is what an *empty* value looks like, and the distinction
// between empty and absent is the one this whole file is careful about.
export function formatValue(type, value) {
  switch (type) {
    case FIELD_BOOL:
      return value === true ? BOOL_TRUE : BOOL_FALSE;
    case FIELD_LIST_TEXT:
      return Array.isArray(value) ? value.join(LIST_SEPARATOR) : String(value);
    case FIELD_NUMBER:
      return typeof value === "number" ? String(value) : String(value);
    case FIELD_TEXT:
    case FIELD_LONGTEXT:
    case FIELD_ENUM:
      return typeof value === "string" ? value : JSON.stringify(value);
    default:
      // A type this interface has not been taught is still shown: an
      // unknown type is a reason to render the JSON text, never a reason
      // to hide a value the game holds.
      return typeof value === "string" ? value : JSON.stringify(value);
  }
}

// fieldRows is the fields of one entity, **in the order its type
// declares them**, with every declared field present as a row.
//
// A value that is `null` reads as absent, which is what
// internal/metamodel/schema.go's own Validate says null means everywhere
// else in this product: "not set". A field the entity carries that its
// type no longer declares is appended at the end and marked
// `undeclared` — it is why the row is flagged invalid, and dropping it
// would hide the reason from the one page that can explain it.
export function fieldRows(schema, entity) {
  const declared = Array.isArray(schema) ? schema : [];
  const values = entity && typeof entity.fields === "object" && entity.fields !== null ? entity.fields : {};
  const seen = new Set();
  const rows = declared
    .filter((field) => field && typeof field.key === "string")
    .map((field) => {
      seen.add(field.key);
      const has = Object.prototype.hasOwnProperty.call(values, field.key) && values[field.key] !== null;
      return {
        key: field.key,
        label: field.label || field.key,
        type: String(field.type ?? ""),
        required: field.required === true,
        undeclared: false,
        cell: has
          ? presentCell(field.key, formatValue(field.type, values[field.key]), values[field.key])
          : absentCell(field.key),
      };
    });
  for (const key of Object.keys(values).sort()) {
    if (seen.has(key)) continue;
    rows.push({
      key,
      label: key,
      type: "",
      required: false,
      undeclared: true,
      cell: presentCell(key, formatValue("", values[key]), values[key]),
    });
  }
  return rows;
}

// relationGroups groups one direction's edges by relation type.
//
// The group is the relation type, because a typed edge is a first-class
// idea here: "requires" and "unlocks" are two different statements about
// the same pair of entities and a flat list would read as one. Each row
// carries the edge's **own** fields, in key order — the schema they
// answer to is a call per group, and a row on a summary page is not
// worth one; `relation_types.get` is where the declared order lives, and
// the catalogue links to it.
export function relationGroups(relations, direction) {
  const items = Array.isArray(relations) ? relations : [];
  const groups = new Map();
  for (const relation of items) {
    if (!relation || typeof relation !== "object") continue;
    const type = String(relation.type_key ?? "");
    if (!groups.has(type)) groups.set(type, { type, direction, rows: [] });
    const far = direction === DIRECTION_OUT ? relation.target : relation.source;
    const fields = relation.fields && typeof relation.fields === "object" ? relation.fields : {};
    groups.get(type).rows.push({
      // The far end, in the terms the edge was written with. A nil ref
      // is not an error: the endpoint read happens after the page was
      // listed, so an entity removed in between leaves an edge whose far
      // end no longer exists. It is said plainly rather than rendered as
      // a row named "".
      far: far && typeof far === "object" ? { type: String(far.type_key ?? ""), key: String(far.key ?? ""), name: String(far.name ?? "") } : null,
      invalid: relation.invalid === true,
      fields: Object.keys(fields)
        .sort()
        .map((key) => ({ key, cell: presentCell(key, formatValue("", fields[key]), fields[key]) })),
    });
  }
  return [...groups.values()].sort((a, b) => (a.type < b.type ? -1 : a.type > b.type ? 1 : 0));
}

// readEntity is every call this page and the panel make, and there are
// four: the entity, its type's schema, the edges either side of it and
// the documents attached to it. **None of them is a view run.**
export async function readEntity(client, typeKey, key) {
  const entity = await client.getEntity(typeKey, key);
  if (!entity.ok) return { ok: false, error: entity.error };
  const [type, out, into, docs] = await Promise.all([
    client.getType(typeKey),
    client.listRelations({ sourceType: typeKey, sourceKey: key, verbose: true }),
    client.listRelations({ targetType: typeKey, targetKey: key, verbose: true }),
    client.listEntityDocs(typeKey, key, {}),
  ]);
  return {
    ok: true,
    entity: entity.result,
    // A schema that could not be read is no schema rather than a wrong
    // one: the fields the entity carries are still shown, as undeclared
    // rows, which is the honest reading of "this page does not know what
    // this type declares".
    schema: type.ok && Array.isArray(type.result.field_schema) ? type.result.field_schema : [],
    out: out.ok && Array.isArray(out.result.items) ? out.result.items : [],
    in: into.ok && Array.isArray(into.result.items) ? into.result.items : [],
    documents: docs.ok && Array.isArray(docs.result.documents) ? docs.result.documents : [],
  };
}

// --- The drawing -----------------------------------------------------

// fieldList paints the rows into a definition list. The label is the
// term and the value the definition, which is what a field *is*; an
// absent value wears the class the twin's own cell carries, so the em
// dash is never the only carrier of the fact.
export function fieldList(doc, rows) {
  const list = doc.createElement("dl");
  list.className = "fields";
  for (const field of rows) {
    const term = doc.createElement("dt");
    term.textContent = field.label;
    if (field.undeclared) term.className = "undeclared";
    list.append(term);

    const value = doc.createElement("dd");
    value.textContent = field.cell.text;
    value.className = field.cell.absent ? "absent" : "";
    list.append(value);

    const kind = doc.createElement("dd");
    kind.className = "field-type";
    kind.textContent = field.type;
    list.append(kind);
  }
  return list;
}

// relationList paints one direction. The relation type heads its group,
// the far end is a link to that entity's own page, and the edge's fields
// follow it as text.
export function relationList(doc, slug, groups) {
  const root = doc.createElement("div");
  for (const group of groups) {
    const heading = doc.createElement("h3");
    heading.className = "relation-type";
    heading.textContent = group.type;
    root.append(heading);

    const count = doc.createElement("p");
    count.className = "muted";
    count.textContent = countLabel(group.rows.length, "relation", "relations");
    root.append(count);

    const list = doc.createElement("ul");
    list.className = "catalogue";
    for (const edge of group.rows) {
      const item = doc.createElement("li");
      if (edge.far === null) {
        const gone = doc.createElement("span");
        gone.className = "catalogue-label absent";
        gone.textContent = "This end is no longer in the game.";
        item.append(gone);
      } else {
        const link = doc.createElement("a");
        link.className = "catalogue-label";
        link.href = entityURL(slug, edge.far.type, edge.far.key);
        link.textContent = edge.far.name || edge.far.key;
        item.append(link);

        const handle = doc.createElement("code");
        handle.className = "catalogue-key";
        handle.textContent = edge.far.type + "/" + edge.far.key;
        item.append(handle);
      }
      for (const field of edge.fields) {
        const cell = doc.createElement("span");
        cell.className = "relation-field";
        cell.textContent = field.key + " " + field.cell.text;
        item.append(cell);
      }
      if (edge.invalid) {
        const flag = doc.createElement("span");
        flag.className = "catalogue-invalid";
        flag.textContent = "invalid";
        item.append(flag);
      }
      list.append(item);
    }
    root.append(list);
  }
  return root;
}

// entityBody is the whole page under the heading, and it is what the
// side panel over a canvas shows too. One builder for both, so the panel
// cannot come to say something the page does not.
export function entityBody(doc, slug, model) {
  const root = doc.createElement("div");
  root.className = "entity";

  const rows = fieldRows(model.schema, model.entity);
  const fields = doc.createElement("section");
  const fieldsHeading = doc.createElement("h2");
  fieldsHeading.textContent = "Fields";
  fields.append(fieldsHeading);
  if (rows.length === 0) {
    const none = doc.createElement("p");
    none.className = "muted";
    none.textContent = "This type declares no fields.";
    fields.append(none);
  } else {
    fields.append(fieldList(doc, rows));
  }
  root.append(fields);

  for (const [direction, heading, relations] of [
    [DIRECTION_OUT, "Relations out", model.out],
    [DIRECTION_IN, "Relations in", model.in],
  ]) {
    const section = doc.createElement("section");
    const title = doc.createElement("h2");
    title.textContent = heading;
    section.append(title);
    const groups = relationGroups(relations, direction);
    if (groups.length === 0) {
      const none = doc.createElement("p");
      none.className = "muted";
      none.textContent =
        direction === DIRECTION_OUT ? "Nothing leads out of this entity." : "Nothing leads into this entity.";
      section.append(none);
    } else {
      section.append(relationList(doc, slug, groups));
    }
    root.append(section);
  }

  const docs = doc.createElement("section");
  const docsHeading = doc.createElement("h2");
  docsHeading.textContent = "Documents";
  docs.append(docsHeading);
  if (model.documents.length === 0) {
    const none = doc.createElement("p");
    none.className = "muted";
    none.textContent = "No document is attached to this entity.";
    docs.append(none);
  } else {
    const list = doc.createElement("ul");
    list.className = "catalogue";
    for (const document of model.documents) {
      const item = doc.createElement("li");
      const anchor = doc.createElement("a");
      anchor.className = "catalogue-label";
      anchor.href = docURL(slug, document.path ?? "");
      anchor.textContent = document.path ?? "";
      item.append(anchor);
      // The preview line: what the document calls itself, what kind it
      // is filed under and the role it plays for this entity. A path
      // says where prose lives and none of those three.
      const preview = doc.createElement("span");
      preview.className = "catalogue-count";
      preview.textContent = [document.title, document.kind, document.role].filter((part) => part).join(" · ");
      item.append(preview);
      list.append(item);
    }
    docs.append(list);
  }
  root.append(docs);

  return root;
}

// --- The page --------------------------------------------------------

export async function entityPage(opened) {
  const doc = opened.document;
  const nameEl = doc.getElementById("entity-name");
  const addressEl = doc.getElementById("entity-address");
  const errorEl = doc.getElementById("entity-error");
  const contentEl = doc.getElementById("entity-content");

  if (opened.game === null) {
    say(nameEl, "Game not found");
    say(errorEl, opened.failure ?? "You may not have access to this game, or it no longer exists.");
    return opened;
  }

  const [typeKey, key] = segmentsOf(opened.location.pathname).slice(1);
  if (!typeKey || !key) {
    say(nameEl, "No entity asked for");
    fail(errorEl, contentEl, "This address names no entity.");
    return opened;
  }

  const model = await readEntity(opened.client, typeKey, key);
  if (!model.ok) {
    if (expired(model)) {
      goToLogin();
      return opened;
    }
    say(nameEl, "Could not read this entity");
    fail(errorEl, contentEl, model.error.message);
    return opened;
  }

  say(nameEl, model.entity.name || model.entity.key);
  say(addressEl, model.entity.type_key + " · " + model.entity.key);
  const back = doc.getElementById("back-to-type");
  if (back) back.href = typeURL(opened.slug, typeKey);

  // The shell's own sections are replaced wholesale by the one builder
  // the panel shares, so the two cannot drift.
  if (contentEl) {
    contentEl.replaceChildren(entityBody(doc, opened.slug, model));
    contentEl.hidden = false;
  }
  if (model.entity.invalid === true) say(errorEl, INVALID_NOTE);
  return opened;
}

if (globalThis.document && globalThis.document.getElementById("entity-name")) {
  const opened = await openGame();
  if (opened !== null) {
    const doc = opened.document;
    if (opened.game !== null) doc.body.prepend(destinations(doc, opened.slug, DESTINATION_CATALOGUE));
    await entityPage(opened);
  }
}

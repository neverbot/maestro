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
import { CATALOGUE_CELLS, row } from "../rows.js";
import {
  DESTINATION_CATALOGUE,
  ROLE_VIEWER,
  STATE_EMPTY,
  countLabel,
  destinations,
  docURL,
  entityURL,
  expired,
  fail,
  gameURL,
  negativeState,
  openGame,
  say,
  segmentsOf,
  setBreadcrumb,
  setReadOnly,
  typeURL,
  typesURL,
} from "./page.js";
import { goToLogin, setFormBusy } from "../app.js";

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
export function relationGroups(relations, direction, labels) {
  const items = Array.isArray(relations) ? relations : [];
  const groups = new Map();
  for (const relation of items) {
    if (!relation || typeof relation !== "object") continue;
    const type = String(relation.type_key ?? "");
    if (!groups.has(type)) {
      const label = labels && typeof labels.get === "function" ? labels.get(type) : "";
      groups.set(type, { type, label: label || "", direction, rows: [] });
    }
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
  const [type, out, into, docs, relationTypes] = await Promise.all([
    client.getType(typeKey),
    client.listRelations({ sourceType: typeKey, sourceKey: key, verbose: true }),
    client.listRelations({ targetType: typeKey, targetKey: key, verbose: true }),
    client.listEntityDocs(typeKey, key, {}),
    // The fifth call, and it buys the page the game's own words for its
    // connections: this screen headed each group with the raw key,
    // `preys_on`, in monospace at heading size, while the catalogue one
    // click away called the same thing "Preys on". One thing named two
    // ways on two screens, and the model's spelling won on the screen a
    // designer reads.
    client.listRelationTypes({}),
  ]);
  return {
    ok: true,
    entity: entity.result,
    // A label per relation type, keyed by the key the edges carry. A type
    // the listing did not reach keeps its key, which is what the edge
    // says and better than a blank heading.
    relationLabels: relationTypes.ok
      ? new Map(
          (relationTypes.result.items || []).map((entry) => [
            String(entry.key ?? ""),
            String(entry.label || entry.key || ""),
          ]),
        )
      : new Map(),
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
  // **One list, not one per relation type.** Each group used to build its
  // own `ul.catalogue`, and sibling grids share nothing: two groups on
  // one entity put their keys 25px apart, on a page whose whole claim is
  // that a column is a column. They are one grid now, with a heading row
  // per type inside it — the same shape `headerRow` gives a catalogue —
  // so every row on the page lines up and a type with three edges costs
  // one row of chrome instead of a heading, a count and a bordered box.
  const list = doc.createElement("ul");
  list.className = "catalogue";
  for (const group of groups) {
    const head = doc.createElement("li");
    head.className = "catalogue-head";

    // The label the game gave this connection, with the key an agent
    // addresses it by beside it in mono — the same pair every row in
    // this product shows, rather than the key alone at heading size.
    const label = doc.createElement("span");
    label.textContent = group.label || group.type;
    head.append(label);

    const key = doc.createElement("code");
    key.className = "catalogue-key";
    key.textContent = group.label && group.label !== group.type ? group.type : "";
    head.append(key);

    // The three content tracks a row has, so the heading is a row of the
    // same grid and not a shape of its own.
    for (let i = 0; i < CATALOGUE_CELLS; i += 1) {
      head.append(doc.createElement("span"));
    }

    const tally = doc.createElement("span");
    tally.className = "catalogue-count";
    tally.textContent = countLabel(group.rows.length, "relation", "relations");
    head.append(tally);
    list.append(head);

    for (const edge of group.rows) {
      // **The shared row.** These were hand-built with a variable number
      // of children — one or two, plus a cell per relation field, plus a
      // flag — into a list whose grid has six tracks, so the rows of one
      // group did not line up with the rows of the next.
      const item = row(doc, {
        label: edge.far === null ? "This end is no longer in the game" : edge.far.name || edge.far.key,
        key: edge.far === null ? "" : edge.far.type + "/" + edge.far.key,
        cells: edge.fields.map((field) => ({ text: field.key + " " + field.cell.text })),
        count: "",
        flag: edge.invalid ? "invalid" : "",
        href: edge.far === null ? "" : entityURL(slug, edge.far.type, edge.far.key),
      });
      // A row whose far end is gone is an absence and says so in the one
      // treatment this product spends on one.
      if (edge.far === null) {
        const gone = item.querySelector ? item.querySelector(".catalogue-label") : null;
        if (gone) gone.classList.add("absent");
      }
      list.append(item);
    }
  }
  return list;
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
    // **The shared negative state, not a muted sentence.** This page
    // builds its own body and replaces the shell's content wholesale, so
    // the four `.state` blocks written into entity.html never rendered:
    // `document.querySelectorAll('.state').length` was 0 on every entity
    // page in the product, and what a reader got instead was three bare
    // grey lines. Their copy is here now, which is what the shell was
    // carrying — including the half that says *who would put one there*,
    // which the muted lines had dropped.
    fields.append(negativeState(doc, {
      kind: STATE_EMPTY,
      heading: "No fields",
      sentence: "This type declares no fields.",
    }));
  } else {
    fields.append(fieldList(doc, rows));
  }
  root.append(fields);

  for (const [direction, heading, relations] of [
    [DIRECTION_OUT, "Leading out of this", model.out],
    [DIRECTION_IN, "Pointing at this", model.in],
  ]) {
    const section = doc.createElement("section");
    const title = doc.createElement("h2");
    title.textContent = heading;
    section.append(title);
    const groups = relationGroups(relations, direction, model.relationLabels);
    if (groups.length === 0) {
      section.append(negativeState(doc, {
        kind: STATE_EMPTY,
        heading: direction === DIRECTION_OUT ? "Nothing leads out" : "Nothing leads in",
        sentence: direction === DIRECTION_OUT
          ? "No relation starts here. An agent writes relations over MCP; nothing on this page does."
          : "No relation points at this. An agent writes relations over MCP; nothing on this page does.",
      }));
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
    docs.append(negativeState(doc, {
      kind: STATE_EMPTY,
      heading: "No prose attached",
      // The half the muted line dropped: who would put one here. It is
      // the sentence the shell had been carrying and nobody ever saw.
      sentence: "No document is attached to this entity. An agent attaches one over MCP; nothing on this page attaches one.",
    }));
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

// --- The one write a person makes here --------------------------------

// wireRename is the product's first write by a person: an entity's own
// name, on the screen that shows it.
//
// The whole of the design it obeys is in
// `.superpowers/specs/2026-09-11-writing-in-the-interface-design.md`,
// and it comes to two rules. **The edit is never lost**: a refusal
// leaves what was typed in the field, because the person's sentence is
// the one thing in the exchange that exists nowhere else. **The edit
// never silently wins**: nothing here re-reads a version and writes
// again on its own, which would be last-writer-wins with extra steps and
// nobody told. A conflict is shown, both values are named, and the next
// move is theirs.
export function wireRename(doc, opened, model) {
  const actions = doc.getElementById("page-actions");
  const form = doc.getElementById("rename");
  const field = doc.getElementById("rename-name");
  const errorEl = doc.getElementById("rename-error");
  const conflict = doc.getElementById("rename-conflict");
  const nameEl = doc.getElementById("entity-name");
  if (!actions || !form || !field) return null;

  // The version this page read. Every write states it, and it advances
  // only when the server says a write landed.
  let entity = { ...model.entity };

  const open = doc.createElement("button");
  open.type = "button";
  open.className = "ghost";
  open.textContent = "Rename";
  actions.replaceChildren(open);

  const show = (showing) => {
    form.hidden = !showing;
    open.hidden = showing;
    if (showing) {
      field.value = entity.name || "";
      say(errorEl, "");
      if (conflict) conflict.hidden = true;
      field.focus();
      if (typeof field.select === "function") field.select();
    }
  };

  open.addEventListener("click", () => show(true));
  const cancel = doc.getElementById("rename-cancel");
  if (cancel) cancel.addEventListener("click", () => show(false));

  // Applied after a write the server accepted: the name on the screen,
  // the tab, the last crumb and the version the next write will state.
  const settle = (row) => {
    entity = { ...entity, name: row.name, version: row.version };
    say(nameEl, entity.name || entity.key);
    doc.title = (entity.name || entity.key) + " \u00b7 Maestro";
    const crumbs = doc.getElementById("crumbs");
    const last = crumbs && crumbs.lastElementChild ? crumbs.lastElementChild : null;
    if (last) last.textContent = entity.name || entity.key;
    show(false);
  };

  // **A batch answers 200 with a list of what it refused.** The route
  // this write goes through is the bulk one, and a stale
  // `expected_version` comes back as `{failed: [{code:
  // "version_conflict"}]}` under an OK status — not as a 409. Read as a
  // success, which is what the first version of this function did, the
  // screen showed the typed name over a row the server had kept: the
  // page lying about a write is the one outcome worse than refusing it.
  const write = async (name) => {
    const answer = await opened.client.renameEntity(entity, name);
    if (!answer.ok) {
      if (expired(answer)) {
        goToLogin();
        return { done: false };
      }
      return { done: false, message: answer.error ? answer.error.message : "" };
    }
    const failed = Array.isArray(answer.result.failed) ? answer.result.failed[0] : null;
    if (failed) {
      if (failed.code === "version_conflict") return { done: false, conflict: true };
      return { done: false, message: String(failed.message ?? "") };
    }
    const written = Array.isArray(answer.result.written) ? answer.result.written[0] : null;
    settle({ name, version: written && Number.isFinite(written.version) ? written.version : entity.version + 1 });
    return { done: true };
  };

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const typed = field.value.trim();
    if (typed === "") {
      say(errorEl, "A name is what this screen is for; it cannot be empty.");
      return;
    }
    say(errorEl, "");
    setFormBusy(form, true, "Saving…");
    const answer = await write(typed);
    setFormBusy(form, false);
    if (answer.done) return;
    if (answer.conflict === true) {
      await showConflict(doc, opened, entity, typed, settle);
      return;
    }
    say(errorEl, answer.message ?? "");
  });
  return form;
}

// showConflict reads what the row says now and puts the two values side
// by side, with two actions and no default.
//
// **When the two are equal the conflict is not shown at all**: the other
// writer wrote the same words, the person's intent already holds, and a
// refusal with no consequence is noise.
export async function showConflict(doc, opened, entity, typed, settle) {
  const box = doc.getElementById("rename-conflict");
  const form = doc.getElementById("rename");
  const errorEl = doc.getElementById("rename-error");
  const current = await opened.client.getEntity(entity.type_key, entity.key);
  if (!current.ok) {
    say(errorEl, current.error.message);
    return null;
  }
  const theirs = current.result.name || current.result.key;
  if (theirs === typed) {
    settle({ name: theirs, version: current.result.version });
    return null;
  }
  if (!box) return null;
  say(doc.getElementById("conflict-theirs"), theirs);
  say(doc.getElementById("conflict-yours"), typed);
  box.hidden = false;
  if (form) form.hidden = true;

  const keep = doc.getElementById("conflict-keep");
  const take = doc.getElementById("conflict-take");
  // **The only write in this product that states a version the person
  // did not read on a page.** It is a decision and not a retry: they
  // have just been shown both values and chosen one, in the same
  // gesture that sends it.
  if (keep) {
    keep.onclick = async () => {
      const again = await opened.client.renameEntity(
        { ...entity, version: current.result.version, fields: current.result.fields },
        typed,
      );
      box.hidden = true;
      const refused = again.ok && Array.isArray(again.result.failed) ? again.result.failed[0] : null;
      if (again.ok && !refused) {
        settle({ name: typed, version: current.result.version + 1 });
        return;
      }
      // Refused again: somebody wrote a third time between the read
      // above and this write. The edit is still in the field, and the
      // decision is still theirs.
      if (form) form.hidden = false;
      say(errorEl, refused ? String(refused.message ?? "") : again.error ? again.error.message : "");
    };
  }
  if (take) {
    take.onclick = () => {
      box.hidden = true;
      settle({ name: theirs, version: current.result.version });
    };
  }
  return box;
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

  const entityName = model.entity.name || model.entity.key;
  say(nameEl, entityName);
  doc.title = entityName + " \u00b7 Maestro";
  say(addressEl, model.entity.type_key + " · " + model.entity.key);
  // One extra call, for the one sentence this screen owes: what it
  // will not let you change, and whether that is the product or your
  // role saying so.
  const role = await opened.client.summary();
  const mayWrite = role.ok && role.result.role !== ROLE_VIEWER;
  // **The notice is a claim about the screen**, so a screen that has
  // gained a write loses the part of the claim that said it had none.
  // A viewer still gets the notice, because for them it is still true:
  // the instance will refuse the write, and offering a control that
  // cannot succeed is worse than saying so.
  if (mayWrite) wireRename(doc, opened, model);
  else if (role.ok) setReadOnly(doc, role.result.role, "writes this entity");
  // Four crumbs, and the third is the type's **key** rather than its
  // plural label. The entity model carries `type_key` and not the type's
  // label, and fetching the type for a word in a trail would be a second
  // round trip on every entity page in the product. The key is also what
  // the address bar says and what the line directly under this heading
  // repeats, so a reader meets it twice rather than meeting a word the
  // page had to pay for.
  setBreadcrumb(doc, [
    { label: opened.game.name, href: gameURL(opened.slug) },
    { label: DESTINATION_CATALOGUE, href: typesURL(opened.slug) },
    { label: typeKey, href: typeURL(opened.slug, typeKey) },
    { label: model.entity.name || model.entity.key },
  ]);

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
  const opened = await openGame({ destination: DESTINATION_CATALOGUE });
  if (opened !== null) {
    const doc = opened.document;
    await entityPage(opened);
  }
}

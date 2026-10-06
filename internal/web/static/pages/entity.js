// One entity: its name, its address, its fields, the edges either side
// of it, and the prose attached to it.

import { absentCell, absentTextFor, presentCell } from "../render/twin.js";
import { t } from "../i18n.js";
import { ABSENT_MARK, boolMark } from "../rows.js";
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
import { logBand } from "../log.js";
import { proseBlock } from "../prose.js";

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
export const BOOL_TRUE = t("entity.bool.yes");
export const BOOL_FALSE = t("entity.bool.no");

// The separator a list of text is joined with.
export const LIST_SEPARATOR = ", ";

// What the two relation lists are called, in the model, so the page and
// the harness name them the same way.
export const DIRECTION_OUT = "out";
export const DIRECTION_IN = "in";

// The line the catalogue page and this one both need: an entity that no
// longer fits its type is kept and marked, never deleted, and this is
// where a designer meets that fact about one row.
export const INVALID_NOTE = t("entity.invalid");

// --- The model -------------------------------------------------------

// formatValue renders one value by its declared type.
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
      // to hide a value the game holds. **A boolean is the exception**,
      // because JSON spells it `true` and that is a word in one language
      // on a screen in another — and this branch is where an edge's
      // fields arrive, the relation type listing being slim enough to
      // carry no schema.
      if (typeof value === "boolean") return value ? BOOL_TRUE : BOOL_FALSE;
      // And a list, for the same reason: `["rlmud","ancient-kingdoms"]`
      // is JSON's punctuation around two words a reader wanted, and the
      // same value one declared type away reads as `rlmud,
      // ancient-kingdoms`. An edge's fields arrive here because the slim
      // relation type listing carries no schema, not because the value
      // is of an unknown kind.
      if (Array.isArray(value)) return value.join(LIST_SEPARATOR);
      return typeof value === "string" ? value : JSON.stringify(value);
  }
}

// fieldRows is the fields of one entity, **in the order its type
// declares them**, with every declared field present as a row.
export function fieldRows(schema, entity) {
  const declared = Array.isArray(schema) ? schema : [];
  const values = entity && typeof entity.fields === "object" && entity.fields !== null ? entity.fields : {};
  const rendered = entity && typeof entity.fields_html === "object" && entity.fields_html !== null
    ? entity.fields_html
    : {};
  const seen = new Set();
  const rows = declared
    .filter((field) => field && typeof field.key === "string")
    .map((field) => {
      seen.add(field.key);
      const has = Object.prototype.hasOwnProperty.call(values, field.key) && values[field.key] !== null;
      const cell = has
        ? presentCell(field.key, formatValue(field.type, values[field.key]), values[field.key])
        : absentCell(field.key);
      // The rendering the browser's own route sent for this value. It is
      // read by key and not decided by type a second time: the server
      // renders exactly the longtext fields a row has a value for, and a
      // key with no entry is a value this page shows as text.
      if (has) {
        const html = rendered[field.key];
        if (typeof html === "string" && html !== "") cell.html = html;
        // A boolean is drawn here too, and not only in a catalogue: one
        // value spelled two ways on two screens is the defect this
        // product keeps reporting against itself.
        if (field.type === FIELD_BOOL && typeof values[field.key] === "boolean") {
          cell.mark = values[field.key] ? "yes" : "no";
          cell.name = values[field.key] ? BOOL_TRUE : BOOL_FALSE;
        }
      }
      return {
        key: field.key,
        label: field.label || field.key,
        type: String(field.type ?? ""),
        required: field.required === true,
        undeclared: false,
        cell,
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
      // **By key, not by position.** These were a positional list, and
      // the edges of one type do not all carry the same fields: an edge
      // with `acquisition` and one with `notes` put two different things
      // in the same column of the same table, and no header could have
      // been right for both.
      values: new Map(Object.keys(fields).map((key) => [key, presentCell(key, formatValue("", fields[key]), fields[key])])),
    });
  }
  // The columns a group draws: every field key any of its edges carries,
  // sorted. **Sorted and not first seen**, so two readings of the same
  // game put the columns in the same order whatever order the rows came
  // back in. The relation type's own declaration order would be better
  // and is not here: this page reads the slim type listing, which
  // carries no field schema.
  for (const group of groups.values()) {
    const keys = new Set();
    for (const edge of group.rows) {
      for (const key of edge.values.keys()) keys.add(key);
    }
    group.columns = [...keys].sort();
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
    // **A row element, inside a `dl` that HTML allows one in.** The four
    // cells were siblings of the list, so there was nothing a stylesheet
    // could call a row: the way to change a value had to be visible on
    // every line at once because no line could answer the pointer. The
    // wrapper draws no box of its own (`display: contents`), so the grid
    // and its columns are the ones they were.
    const line = doc.createElement("div");
    line.className = "field-row";
    list.append(line);

    const term = doc.createElement("dt");
    term.textContent = field.label;
    if (field.undeclared) term.className = "undeclared";
    line.append(term);

    const value = doc.createElement("dd");
    // `field-value` is what wireFieldEdits finds a cell by. The list
    // stays presentational — it paints a model and knows nothing about
    // writing — exactly as the page head knows nothing about the rename
    // that replaces its button.
    value.className = field.cell.absent ? "field-value absent" : "field-value";
    if (typeof field.cell.html === "string" && field.cell.html !== "") {
      value.classList.add("field-prose");
      value.append(proseBlock(doc, field.cell.html));
    } else if (field.cell.mark) {
      value.classList.add("marked");
      value.setAttribute("aria-label", field.cell.name || field.cell.text);
      value.append(boolMark(doc, field.cell.mark));
    } else {
      value.textContent = field.cell.text;
    }
    line.append(value);

    const kind = doc.createElement("dd");
    kind.className = "field-type";
    kind.textContent = field.type;
    line.append(kind);

    // The column the way to change this value lives in, reserved whether
    // or not anything is in it: a track that appears with the control
    // would move the three columns beside it every time a pointer
    // crossed a row.
    const action = doc.createElement("dd");
    action.className = "field-action";
    line.append(action);
  }
  return list;
}

// relationList paints one direction: one table per relation type, with
// the type's own name, key and count above it.
//
// **One table per type, and this reverses a decision taken here.** They
// were one grid with a heading row per type, so that two groups on one
// entity could not put their keys 25px apart. That was right while every
// row carried the same three nameless cells, and it stopped being right
// the moment the cells were named: `grants` declares five fields and
// `open_to` two, so one shared column held `acquisition` in one group
// and `original_muds` in the next — and, inside one group, an edge
// without `acquisition` shifted every value it did carry one column to
// the left. Columns that mean different things down one table cannot be
// headed, and a table nobody can head is a table nobody can read. The
// alignment that is lost was alignment between things that were never
// the same measurement.
//
// A real `<table>`, because that is what this is: the column set belongs
// to the relation type, so there is no fixed template to share with the
// catalogue, and the browser sizes columns from the content for free.
// `mst-twin` and `mst-table` already hold the same shape from the same
// tokens; the row height and the rules are theirs.
export function relationList(doc, slug, groups) {
  const holder = doc.createElement("div");
  holder.className = "edge-groups";
  for (const group of groups) {
    const section = doc.createElement("section");
    section.className = "edge-group";

    const table = doc.createElement("table");
    table.className = "edges";

    // **The table names itself.** This was a heading floating above a
    // bordered box, which is two objects where a reader sees one, and it
    // left the table unnamed for anybody not looking at it. A caption is
    // the table's own name; the panel's border closes around both.
    const head = doc.createElement("caption");
    head.className = "edge-head";
    // **The caption keeps its own display and the flex goes inside it.**
    // `display: flex` on a `<caption>` takes away its table-caption role,
    // and the box is then laid out inside the table's own flow: the name
    // of the table rendered under its column headings.
    const line = doc.createElement("div");
    head.append(line);
    const label = doc.createElement("span");
    label.className = "edge-label";
    label.textContent = group.label || group.type;
    line.append(label);
    const key = doc.createElement("code");
    key.className = "edge-key";
    key.textContent = group.label && group.label !== group.type ? group.type : "";
    line.append(key);
    const tally = doc.createElement("span");
    tally.className = "edge-count";
    tally.textContent = countLabel(group.rows.length, t("unit.relation"), t("unit.relations"));
    line.append(tally);
    table.append(head);

    const header = doc.createElement("tr");
    // The two columns every edge has, then one per field this type's
    // edges carry. A field key is the game's own word and is never
    // translated; the two that are Maestro's are.
    for (const text of [t("column.name"), t("column.id")]) {
      const cell = doc.createElement("th");
      cell.setAttribute("scope", "col");
      cell.textContent = text;
      header.append(cell);
    }
    for (const key of group.columns) {
      const cell = doc.createElement("th");
      cell.setAttribute("scope", "col");
      cell.className = "edge-field";
      cell.textContent = key;
      header.append(cell);
    }
    const headRow = doc.createElement("thead");
    headRow.append(header);
    table.append(headRow);

    const body = doc.createElement("tbody");
    for (const edge of group.rows) {
      const line = doc.createElement("tr");

      const name = doc.createElement("td");
      name.className = "edge-name";
      if (edge.far === null) {
        // A row whose far end is gone is an absence and says so in the
        // one treatment this product spends on one.
        name.classList.add("absent");
        name.textContent = t("entity.endGone");
      } else {
        const link = doc.createElement("a");
        link.href = entityURL(slug, edge.far.type, edge.far.key);
        link.textContent = edge.far.name || edge.far.key;
        name.append(link);
      }
      line.append(name);

      const address = doc.createElement("td");
      address.className = "edge-id";
      address.textContent = edge.far === null ? "" : edge.far.type + "/" + edge.far.key;
      line.append(address);

      for (const column of group.columns) {
        const cell = doc.createElement("td");
        const value = edge.values.get(column);
        if (!value) {
          // An edge of this type that carries no value for this field.
          // The mark, not a blank: a reader has to be able to tell an
          // empty column from one that simply was not filled in here.
          cell.className = "absent";
          cell.textContent = ABSENT_MARK;
        } else {
          cell.textContent = value.text;
        }
        line.append(cell);
      }
      body.append(line);

      if (edge.invalid) {
        // The sentence this product already has for a row that stopped
        // fitting its declaration, rather than the word "invalid", which
        // is English on a screen that is not.
        const note = doc.createElement("tr");
        note.className = "edge-invalid";
        const said = doc.createElement("td");
        said.setAttribute("colspan", String(group.columns.length + 2));
        said.textContent = t("entity.invalid");
        note.append(said);
        body.append(note);
      }
    }
    table.append(body);
    section.append(table);
    holder.append(section);
  }
  return holder;
}

// attachLog reads one thing's log and builds its band. **One function,
// three pages**: the entity, the type and the relation type each carry
// the same band, and the target is the only thing that differs.
export async function attachLog(doc, opened, target, mayWrite, role) {
  const answer = await opened.client.listComments(target);
  const band = logBand(doc, {
    role,
    comments: answer.ok ? answer.result.items : [],
    write: mayWrite
      ? async (body) => {
        const written = await opened.client.addComment(target, body);
        if (written.ok) await refreshLog(band, opened, target);
        return written;
      }
      : undefined,
    remove: mayWrite
      ? async (comment) => {
        const gone = await opened.client.removeComment(comment.id);
        if (gone.ok) await refreshLog(band, opened, target);
      }
      : undefined,
  });
  return band;
}

// refreshLog re-reads rather than splicing the answer in: the server
// renders the markdown, so the page has no way to draw a note it has not
// been handed.
async function refreshLog(band, opened, target) {
  const again = await opened.client.listComments(target);
  if (again.ok && typeof band.draw === "function") band.draw(again.result.items);
}

// entityBody is the whole page under the heading, and it is what the
// side panel over a canvas shows too. One builder for both, so the panel
// cannot come to say something the page does not.
export function entityBody(doc, slug, model) {
  const root = doc.createElement("div");
  root.className = "entity";

  const rows = fieldRows(model.schema, model.entity);
  // **No heading over the fields.** This band is what the page is: the
  // thing's own values, under the thing's own name, which the page title
  // and the breadcrumb have already said. A heading here would name the
  // subject twice and rank the content below the furniture around it.
  const fields = doc.createElement("section");
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
      heading: t("entity.noFields.heading"),
      sentence: t("entity.noFields.sentence"),
    }));
  } else {
    const list = fieldList(doc, rows);
    // Handed back on the root so the writing can find it without
    // querying the document: this body is built for two places — the
    // page and the panel over a canvas — and only one of them is
    // writable, so the wiring is attached by the caller rather than
    // baked in here.
    root.fieldsList = list;
    root.fieldRows = rows;
    fields.append(list);
  }
  fields.className = "paired";
  root.append(fields);

  // **The log's place, between the thing's own values and its edges.**
  // It is filled by the page once the server has answered, and it is
  // here rather than last because what was thought about a thing is not
  // the least of what a page about it holds. On a wide window it sits
  // beside the fields instead of under them.
  const log = doc.createElement("section");
  log.className = "paired";
  root.logSlot = log;
  root.append(log);

  // **One band for the edges, two halves inside it.** They were two
  // sibling bands headed "Leading out of this" and "Pointing at this",
  // which name a direction in the metamodel's terms and leave a reader
  // working out which end they are standing on. The band names what it
  // holds — the entities this one is connected to — and each half says
  // which way the arrow runs, in the only words that are unambiguous
  // from here: towards this, or away from it.
  const related = doc.createElement("section");
  const relatedHeading = doc.createElement("h2");
  relatedHeading.textContent = t("entity.related");
  related.append(relatedHeading);
  // Incoming first: what points at a thing is what a designer opens its
  // page to find — which guild grants this ability, which quest needs
  // this item — and the other half is one click away on each row.
  for (const [direction, heading, relations, absent] of [
    [DIRECTION_IN, t("entity.pointingHere"), model.in,
      { heading: t("entity.nothingIn.heading"), sentence: t("entity.nothingIn.sentence") }],
    [DIRECTION_OUT, t("entity.pointedFromHere"), model.out,
      { heading: t("entity.nothingOut.heading"), sentence: t("entity.nothingOut.sentence") }],
  ]) {
    const half = doc.createElement("section");
    half.className = "related-half";
    const title = doc.createElement("h3");
    title.textContent = heading;
    half.append(title);
    const groups = relationGroups(relations, direction, model.relationLabels);
    if (groups.length === 0) {
      half.append(negativeState(doc, { kind: STATE_EMPTY, ...absent }));
    } else {
      half.append(relationList(doc, slug, groups));
    }
    related.append(half);
  }
  root.append(related);

  const docs = doc.createElement("section");
  const docsHeading = doc.createElement("h2");
  docsHeading.textContent = t("entity.documents");
  docs.append(docsHeading);
  if (model.documents.length === 0) {
    docs.append(negativeState(doc, {
      kind: STATE_EMPTY,
      heading: t("entity.noProse.heading"),
      // The half the muted line dropped: who would put one here. It is
      // the sentence the shell had been carrying and nobody ever saw.
      sentence: t("entity.noProse.sentence"),
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

// --- Writing a field value -------------------------------------------
export const EDIT_LABEL = t("entity.edit");
export const SAVE_LABEL = t("entity.save");
export const CANCEL_LABEL = t("entity.cancel");
export const CLEAR_HINT = t("entity.clearHint");
export const NOT_A_NUMBER = t("entity.notANumber");

// controlFor builds the control one declared type deserves, already
// carrying the value the row holds.
export function controlFor(doc, field, value) {
  const type = String(field && field.type ? field.type : "");
  switch (type) {
    case FIELD_BOOL: {
      const el = doc.createElement("input");
      el.type = "checkbox";
      el.checked = value === true;
      // A checkbox cannot say "absent", and that is honest here: a
      // declared bool a row does not carry is being answered for the
      // first time, and the answer is the box's state.
      return { el, read: () => ({ value: el.checked === true }) };
    }
    case FIELD_ENUM: {
      const el = doc.createElement("select");
      // The empty option is how an enum is cleared, and it is named
      // rather than blank: a blank option in a list of words reads as a
      // rendering fault.
      const none = doc.createElement("option");
      none.value = "";
      none.textContent = t("value.none");
      el.append(none);
      for (const option of Array.isArray(field.options) ? field.options : []) {
        const item = doc.createElement("option");
        item.value = String(option);
        item.textContent = String(option);
        el.append(item);
      }
      el.value = typeof value === "string" ? value : "";
      return { el, read: () => (el.value === "" ? { absent: true } : { value: el.value }) };
    }
    case FIELD_NUMBER: {
      const el = doc.createElement("input");
      el.type = "number";
      el.value = typeof value === "number" ? String(value) : "";
      return {
        el,
        read: () => {
          const raw = String(el.value ?? "").trim();
          if (raw === "") return { absent: true };
          const parsed = Number(raw);
          // The server would refuse this too, and would be right; the
          // point of refusing it here is that the sentence lands under
          // the control the person is looking at, before a round trip.
          if (!Number.isFinite(parsed)) return { problem: NOT_A_NUMBER };
          return { value: parsed };
        },
      };
    }
    case FIELD_LIST_TEXT: {
      const el = doc.createElement("textarea");
      el.rows = 4;
      el.value = Array.isArray(value) ? value.join("\n") : "";
      return {
        el,
        read: () => {
          const lines = String(el.value ?? "")
            .split("\n")
            .map((line) => line.trim())
            .filter((line) => line !== "");
          return lines.length === 0 ? { absent: true } : { value: lines };
        },
      };
    }
    case FIELD_LONGTEXT: {
      const el = doc.createElement("textarea");
      el.rows = 6;
      el.value = typeof value === "string" ? value : "";
      return { el, read: () => readText(el) };
    }
    default: {
      const el = doc.createElement("input");
      el.type = "text";
      el.value = typeof value === "string" ? value : "";
      return { el, read: () => readText(el) };
    }
  }
}

// readText is the two text controls' shared answer, and it is where the
// absent/empty rule is actually spelled: nothing typed means remove the
// value, not store the empty string.
function readText(el) {
  const raw = String(el.value ?? "");
  return raw === "" ? { absent: true } : { value: raw };
}

// fieldsWith returns the row's whole `fields` object with one key set or
// removed.
export function fieldsWith(fields, key, answer) {
  const next = { ...(fields && typeof fields === "object" ? fields : {}) };
  if (answer && answer.absent === true) delete next[key];
  else next[key] = answer ? answer.value : null;
  return next;
}

// wireFieldEdits turns each declared field's value into something a
// person can change, in place, on the screen they are reading it on.
export function wireFieldEdits(doc, opened, model, schema) {
  const holder = model.fieldsList;
  if (!holder || typeof holder.children === "undefined") return null;

  let entity = { ...model.entity };
  const byKey = new Map();
  for (const field of Array.isArray(schema) ? schema : []) {
    if (field && typeof field.key === "string") byKey.set(field.key, field);
  }

  const rows = Array.isArray(model.rows) ? model.rows : [];
  const kids = [...holder.children];
  const wired = [];
  rows.forEach((row, index) => {
    const line = kids[index];
    const cells = line && line.children ? line.children : [];
    const cell = cells[1];
    const action = cells[3];
    const field = byKey.get(row.key);
    if (!cell || !action || !field || row.undeclared) return;
    wired.push(editable(doc, opened, cell, action, field, {
      current: () => entity,
      settled: (next) => {
        entity = next;
      },
    }));
  });
  return wired;
}

// editable is one field's own little machine: the value, an Edit button,
// and — once pressed — the control, Save, Cancel and a sentence of its
// own.
function editable(doc, opened, cell, action, field, hooks) {
  // **A prose field keeps its rendering when the page becomes writable.**
  // This function replaces the cell's children with its own, so without
  // the three lines below a longtext was rendered by fieldList and
  // flattened back to one line of source the moment the writing was
  // wired — which is to say on every page a member can edit, and on none
  // of the read-only panels the module tests drove.
  const isProse = String(cell.className || "").includes("field-prose");
  const first = isProse && typeof cell.querySelector === "function" ? cell.querySelector(".prose") : null;
  const startHTML = first ? first.innerHTML : "";
  // The same trap the prose met: a drawn boolean is a child of the cell,
  // and this function replaces the cell's children with its own.
  const drawn = typeof cell.querySelector === "function" ? cell.querySelector(".mark") : null;
  const startMark = drawn ? String(drawn.getAttribute("class") || "").replace("mark mark-", "") : "";
  const startName = cell.getAttribute ? cell.getAttribute("aria-label") : "";
  const shown = doc.createElement(isProse ? "div" : "span");
  if (!isProse) shown.textContent = cell.textContent;
  const wasAbsent = String(cell.className || "").includes("absent");

  const open = doc.createElement("button");
  open.type = "button";
  // **Not a ghost.** It carried `ghost field-edit`, which is two
  // vocabularies on one element: `.field-edit` strips the button to a
  // word and `.ghost:hover` dressed it again, so the pointer drew a
  // 32-pixel bordered box with a halo around twelve pixels of text. One
  // vocabulary, and it answers the pointer itself.
  open.className = "field-edit";
  open.textContent = EDIT_LABEL;
  // The name says which field, because "Edit" said eight times on one
  // page is eight controls a screen reader cannot tell apart.
  open.setAttribute("aria-label", EDIT_LABEL + " " + (field.label || field.key));

  const form = doc.createElement("form");
  form.className = "field-edit-form";
  form.hidden = true;

  const error = doc.createElement("p");
  error.className = "error";

  const draw = (value, absent, html, mark, name) => {
    // The class says the cell *holds* a block, not that the field could
    // have one: a longtext somebody cleared is an absence and wears the
    // absence's own treatment, with nothing to render inside it.
    const holds = isProse && typeof html === "string" && html !== "";
    if (holds) {
      shown.replaceChildren(proseBlock(doc, html));
    } else if (mark) {
      shown.replaceChildren(boolMark(doc, mark));
    } else {
      shown.replaceChildren();
      shown.textContent = value;
    }
    const base = holds ? "field-value field-prose" : mark ? "field-value marked" : "field-value";
    cell.className = absent ? base + " absent" : base;
    if (mark) cell.setAttribute("aria-label", name || value);
    else if (cell.removeAttribute) cell.removeAttribute("aria-label");
    cell.replaceChildren(shown, form, error);
    action.replaceChildren(open);
  };
  draw(cell.textContent, wasAbsent, startHTML, startMark, startName);

  let control = null;
  const close = () => {
    form.hidden = true;
    open.hidden = false;
    shown.hidden = false;
    error.textContent = "";
  };

  open.addEventListener("click", () => {
    const entity = hooks.current();
    const values = entity.fields && typeof entity.fields === "object" ? entity.fields : {};
    control = controlFor(doc, field, values[field.key]);
    const save = doc.createElement("button");
    save.type = "submit";
    save.textContent = SAVE_LABEL;
    const cancel = doc.createElement("button");
    cancel.type = "button";
    cancel.className = "ghost";
    cancel.textContent = CANCEL_LABEL;
    cancel.addEventListener("click", () => close());
    const hint = doc.createElement("span");
    hint.className = "muted field-note";
    // Only where clearing is possible, and it is not possible on a
    // checkbox: a bool has two answers and neither of them is "no
    // answer".
    hint.textContent = field.type === FIELD_BOOL ? "" : CLEAR_HINT;
    form.replaceChildren(control.el, save, cancel, hint);
    form.hidden = false;
    open.hidden = true;
    // The value itself steps aside while it is being changed: the
    // control already holds it, and showing both put the old text above
    // a field containing the same words with no statement of which one
    // the page would keep.
    shown.hidden = true;
    if (typeof control.el.focus === "function") control.el.focus();
  });

  form.addEventListener("submit", async (event) => {
    if (event && typeof event.preventDefault === "function") event.preventDefault();
    if (!control) return;
    const read = control.read();
    if (read.problem) {
      error.textContent = read.problem;
      return;
    }
    error.textContent = "";
    setFormBusy(form, true, t("action.saving"));
    const entity = hooks.current();
    const answer = await opened.client.writeEntity(entity, {
      fields: fieldsWith(entity.fields, field.key, read),
    });
    setFormBusy(form, false);

    if (!answer.ok) {
      if (expired(answer)) {
        goToLogin();
        return;
      }
      error.textContent = answer.error ? answer.error.message : "";
      return;
    }
    // **A batch answers 200 with what it refused**, which is the one
    // thing about this route a page has to know. A `schema_violation`
    // carries the path of the value it is about, and the sentence lands
    // here, under that value, rather than at the top of a page whose
    // other seven fields are fine.
    const failed = Array.isArray(answer.result.failed) ? answer.result.failed[0] : null;
    if (failed) {
      if (failed.code === "version_conflict") {
        await resolveFieldConflict(doc, opened, {
          entity,
          field,
          read,
          error,
          settled: async (next) => {
            hooks.settled(next);
            const values = next.fields || {};
            const has = Object.prototype.hasOwnProperty.call(values, field.key) && values[field.key] !== null;
            const current = values[field.key];
            const marked = has && field.type === FIELD_BOOL && typeof current === "boolean";
            draw(has ? formatValue(field.type, current) : absentTextFor(field.label || field.key), !has,
              isProse && has ? await renderedFor(opened, next, field.key) : "",
              marked ? (current ? "yes" : "no") : "", marked ? (current ? BOOL_TRUE : BOOL_FALSE) : "");
            close();
          },
        });
        return;
      }
      error.textContent = String(failed.message ?? "");
      return;
    }

    const written = Array.isArray(answer.result.written) ? answer.result.written[0] : null;
    const next = {
      ...entity,
      fields: fieldsWith(entity.fields, field.key, read),
      version: written && Number.isFinite(written.version) ? written.version : entity.version + 1,
    };
    hooks.settled(next);
    const drawnNow = read.absent !== true && field.type === FIELD_BOOL && typeof read.value === "boolean";
    draw(read.absent === true
      ? absentTextFor(field.label || field.key)
      : formatValue(field.type, read.value), read.absent === true,
    isProse && read.absent !== true ? await renderedFor(opened, next, field.key) : "",
    drawnNow ? (read.value ? "yes" : "no") : "", drawnNow ? (read.value ? BOOL_TRUE : BOOL_FALSE) : "");
    close();
  });

  return { cell, open, form };
}

// renderedFor re-reads the row for one field's rendering. A write answers
// with a batch report and this browser cannot render markdown itself, so
// the alternative is a field that reads as prose until the moment somebody
// edits it and as source afterwards. A failed re-read is not a failure of
// the save: the value landed, and the source the page already holds is
// what it falls back to.
async function renderedFor(opened, entity, key) {
  const again = await opened.client.getEntity(entity.type_key, entity.key);
  if (!again || !again.ok) return "";
  const map = again.result && typeof again.result.fields_html === "object" && again.result.fields_html !== null
    ? again.result.fields_html
    : {};
  const html = map[key];
  return typeof html === "string" ? html : "";
}

// resolveFieldConflict is the rename's conflict, about a value.
async function resolveFieldConflict(doc, opened, spec) {
  const box = doc.getElementById("rename-conflict");
  const current = await opened.client.getEntity(spec.entity.type_key, spec.entity.key);
  if (!current.ok) {
    spec.error.textContent = current.error.message;
    return;
  }
  const values = current.result.fields && typeof current.result.fields === "object" ? current.result.fields : {};
  const has = Object.prototype.hasOwnProperty.call(values, spec.field.key) && values[spec.field.key] !== null;
  const theirs = has
    ? formatValue(spec.field.type, values[spec.field.key])
    : absentTextFor(spec.field.label || spec.field.key);
  const yours = spec.read.absent === true
    ? absentTextFor(spec.field.label || spec.field.key)
    : formatValue(spec.field.type, spec.read.value);

  // The other writer wrote the same value: the person's intent already
  // holds and a refusal with nothing to decide is noise.
  if (theirs === yours) {
    spec.settled({ ...spec.entity, fields: values, version: current.result.version });
    return;
  }
  if (!box) return;
  say(doc.getElementById("conflict-theirs"), theirs);
  say(doc.getElementById("conflict-yours"), yours);
  box.hidden = false;

  const keep = doc.getElementById("conflict-keep");
  const take = doc.getElementById("conflict-take");
  if (keep) {
    keep.onclick = async () => {
      const again = await opened.client.writeEntity(
        { ...spec.entity, version: current.result.version, fields: values },
        { fields: fieldsWith(values, spec.field.key, spec.read) },
      );
      box.hidden = true;
      const refused = again.ok && Array.isArray(again.result.failed) ? again.result.failed[0] : null;
      if (again.ok && !refused) {
        await spec.settled({
          ...spec.entity,
          fields: fieldsWith(values, spec.field.key, spec.read),
          version: current.result.version + 1,
        });
        return;
      }
      spec.error.textContent = refused
        ? String(refused.message ?? "")
        : again.error
          ? again.error.message
          : "";
    };
  }
  if (take) {
    take.onclick = async () => {
      box.hidden = true;
      await spec.settled({ ...spec.entity, fields: values, version: current.result.version });
    };
  }
}

// wireRename is the product's first write by a person: an entity's own
// name, on the screen that shows it.
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
  open.textContent = t("entity.rename");
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
      say(errorEl, t("entity.nameRequired"));
      return;
    }
    say(errorEl, "");
    setFormBusy(form, true, t("action.saving"));
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
    say(nameEl, t("error.gameNotFound"));
    say(errorEl, opened.failure ?? t("error.noAccessToGame"));
    return opened;
  }

  const [typeKey, key] = segmentsOf(opened.location.pathname).slice(1);
  if (!typeKey || !key) {
    say(nameEl, t("entity.noneAsked"));
    fail(errorEl, contentEl, t("entity.noSuchEntity"));
    return opened;
  }

  const model = await readEntity(opened.client, typeKey, key);
  if (!model.ok) {
    if (expired(model)) {
      goToLogin();
      return opened;
    }
    say(nameEl, t("entity.unreadable"));
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
  else if (role.ok) setReadOnly(doc, role.result.role, "writes.entity");
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
    const body = entityBody(doc, opened.slug, model);
    contentEl.replaceChildren(body);
    contentEl.hidden = false;
    // **The field editor is attached after the body exists**, and only
    // on the page: the same builder draws the panel over a canvas, and
    // that panel is a reading surface beside a drawing. A write there
    // would be a second place to make the same mistake in, against a
    // version read for a different purpose.
    if (mayWrite) {
      wireFieldEdits(doc, opened, { ...model, fieldsList: body.fieldsList, rows: body.fieldRows }, model.schema);
    }
    const band = await attachLog(doc, opened, {
      on: "entity", typeKey: model.entity.type_key, key: model.entity.key,
    }, mayWrite, role.ok ? role.result.role : "");
    if (body.logSlot) {
      body.logSlot.replaceWith(band);
      band.classList.add("paired");
      // The pair is set by the page and only when there is a log to pair
      // with, so a body built for the panel over a canvas stays one
      // column.
      body.classList.add("pair");
    }
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

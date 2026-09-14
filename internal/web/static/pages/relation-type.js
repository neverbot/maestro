// One relation type: what it may join, what a walk makes of it, and what
// each of its edges carries.
//
// **It exists because a relation type had no screen at all.** The
// Catalogue listed them as rows that hovered like links and went
// nowhere, so a designer who met `requires` on an entity page — with a
// field value on it, no less — had no way to find out which types it
// joins, whether the analysis engine treats it as a gate, or what that
// field was declared to be. Every one of those facts was already on the
// wire; nothing drew them.
//
// It is a read-only page and says so where a button would be: relation
// types are declared by agents, like everything else in the metamodel.

import {
  DESTINATION_CATALOGUE,
  countLabel,
  expired,
  fillState,
  gameURL,
  openGame,
  say,
  segmentsOf,
  setBreadcrumb,
  setReadOnly,
  typeURL,
  typesURL,
} from "./page.js";
import { headerRow, row } from "../rows.js";
import { goToLogin } from "../app.js";

// The empty state, in the page rather than in the shell. It is not an
// omission being reported: a relation type with no fields is the ordinary
// case, and the sentence says so.
export const NO_FIELDS_HEADING = "No fields";
export const NO_FIELDS_SENTENCE =
  "A relation of this type is the connection itself, and carries no values of its own.";

// ANY_TYPE is what an empty endpoint list means, and it is the one place
// this page says something the server did not: `source_type_keys: []` is
// "no restriction", which is a permission and reads as an omission if it
// is drawn as an empty list.
export const ANY_TYPE = "anything";

// NO_ROLE and NO_TRAITS are the Named Absence Rule applied to the two
// fields the analysis engine reads. An undeclared trait list is not an
// empty one — internal/web/mcp_metamodel.go's own comment on
// AnalysisTraits says so — and a screen that drew both as blank would
// erase the distinction the column exists to carry.
export const NO_ROLE = "No role declared";
export const NO_TRAITS = "No traits declared, so a walk treats it as an ordinary edge";

export function relationTypeKeyOf(pathname) {
  const parts = segmentsOf(pathname);
  return parts[parts.length - 1] || "";
}

// endpointSentence says what may sit at each end, in the product's own
// words rather than in the payload's.
export function endpointSentence(sources, targets) {
  return "From " + joinKeys(sources) + " to " + joinKeys(targets) + ".";
}

// joinKeys is the one spelling of a list of allowed types, and both the
// sentence above and the drawn version below go through it.
//
// It is shared rather than written twice because it was written twice
// for about ten minutes: the sentence said "quest, class" and the page
// drew "quest or class", so the pure function a test asserts and the
// element a reader sees disagreed — a test passing over a screen that
// says something else is worse than no test.
export function joinKeys(keys) {
  const list = Array.isArray(keys) ? keys.filter((key) => key !== "") : [];
  if (list.length === 0) return ANY_TYPE;
  if (list.length === 1) return list[0];
  return list.slice(0, -1).join(", ") + " or " + list[list.length - 1];
}

// roleSentence says what a walk makes of this type. The two halves are
// separate statements and are written as two sentences: a type can carry
// a semantic role and no traits, and the opposite.
export function roleSentence(semanticRole, traits) {
  const declared = Array.isArray(traits) ? traits.filter((trait) => trait !== "") : [];
  const role = typeof semanticRole === "string" && semanticRole !== ""
    ? "Its role is " + semanticRole + "."
    : NO_ROLE + ".";
  const walk = declared.length === 0
    ? NO_TRAITS + "."
    : "A walk reads it as " + declared.join(", ") + ".";
  return role + " " + walk;
}

// fieldRows draws the schema as the same catalogue row every other
// listing uses: the key in the mono slot a person copies from, the
// declared type, whether it is required, and the default when there is
// one.
export function fieldRows(doc, schema) {
  const fields = Array.isArray(schema) ? schema : [];
  return fields.map((field) =>
    row(doc, {
      label: field.label || field.key,
      key: field.key || "",
      cells: [
        { text: String(field.type ?? "") },
        field.required === true ? { text: "required" } : { text: "optional", absent: true },
        // **A declared default is a value and `false` is one of them.**
        // Reading `field.default` for truth would draw "no default" over
        // a field that defaults to false or to zero, which is the defect
        // internal/metamodel/schema.go grew `HasDefault` to prevent —
        // and this page would have re-introduced it one layer up.
        Object.prototype.hasOwnProperty.call(field, "default")
          ? { text: JSON.stringify(field.default) }
          : { text: "no default", absent: true },
      ],
      count: "",
    }),
  );
}

// paintEndpoints writes the sentence with each named type as a link to
// its own catalogue. The words between the links are this page's own —
// "From", "to" — exactly as endpointSentence writes them, so the spoken
// sentence and the drawn one are the same sentence.
export function paintEndpoints(doc, el, slug, sources, targets) {
  if (!el) return;
  el.replaceChildren();
  el.append(text(doc, "From "));
  appendKeys(doc, el, slug, sources);
  el.append(text(doc, " to "));
  appendKeys(doc, el, slug, targets);
  el.append(text(doc, "."));
}

function appendKeys(doc, el, slug, keys) {
  const list = Array.isArray(keys) ? keys.filter((key) => key !== "") : [];
  if (list.length === 0) {
    // An empty list is a permission and not an omission: this type may
    // join anything. It is a word and never a blank, the Named Absence
    // Rule applied to a rule rather than to a value. The word is
    // joinKeys', so the drawn sentence and the spoken one cannot drift.
    el.append(text(doc, joinKeys(list)));
    return;
  }
  list.forEach((key, index) => {
    if (index > 0) el.append(text(doc, index === list.length - 1 ? " or " : ", "));
    const link = doc.createElement("a");
    link.href = typeURL(slug, key);
    link.textContent = key;
    el.append(link);
  });
}

// text is a text node, or the span this product's DOM stub can make when
// there is no createTextNode — every string still goes in through
// textContent, which is rows.js's rule and this file's.
function text(doc, value) {
  if (typeof doc.createTextNode === "function") return doc.createTextNode(value);
  const span = doc.createElement("span");
  span.textContent = value;
  return span;
}

export async function relationTypePage(opened) {
  const doc = opened.document;
  const nameEl = doc.getElementById("relation-type-name");
  const metaEl = doc.getElementById("relation-type-meta");
  const errorEl = doc.getElementById("relation-type-error");

  if (opened.game === null) {
    say(nameEl, "Game not found");
    say(metaEl, opened.failure ?? "You may not have access to this game, or it no longer exists.");
    return opened;
  }

  const key = relationTypeKeyOf(opened.location.pathname);
  setBreadcrumb(doc, [
    { label: opened.game.name, href: gameURL(opened.slug) },
    { label: DESTINATION_CATALOGUE, href: typesURL(opened.slug) },
    { label: key },
  ]);

  const answer = await opened.client.getRelationType(key);
  if (!answer.ok) {
    if (expired(answer)) {
      goToLogin();
      return opened;
    }
    say(nameEl, "Could not read this relation type");
    if (errorEl) {
      errorEl.textContent = answer.error.message;
      errorEl.hidden = false;
    }
    return opened;
  }

  const type = answer.result;
  const name = type.label || type.key;
  say(nameEl, name);
  doc.title = name + " · Maestro";
  setBreadcrumb(doc, [
    { label: opened.game.name, href: gameURL(opened.slug) },
    { label: DESTINATION_CATALOGUE, href: typesURL(opened.slug) },
    { label: name },
  ]);
  say(metaEl, type.key);
  say(doc.getElementById("relation-type-about"), String(type.description ?? ""));

  // **Each end is a link to that type's catalogue**, because a designer
  // reading "from quest to zone" is one click from wanting the zones,
  // and because this page exists to be the place those two words stop
  // being opaque.
  paintEndpoints(doc, doc.getElementById("relation-type-endpoints"), opened.slug,
    type.source_type_keys, type.target_type_keys);

  say(doc.getElementById("relation-type-role"), roleSentence(type.semantic_role, type.analysis_traits));

  fillState(doc, "relation-type-fields-empty", {
    heading: NO_FIELDS_HEADING,
    sentence: NO_FIELDS_SENTENCE,
  });

  const listEl = doc.getElementById("relation-type-fields");
  const emptyEl = doc.getElementById("relation-type-fields-empty");
  const rows = fieldRows(doc, type.field_schema);
  if (listEl) {
    listEl.replaceChildren();
    if (rows.length > 0) {
      listEl.append(
        headerRow(doc, {
          label: "Field",
          key: "key",
          cells: [{ text: "Type" }, { text: "Required" }, { text: "Default" }],
        }),
      );
      for (const line of rows) listEl.append(line);
    }
    listEl.hidden = rows.length === 0;
  }
  if (emptyEl) emptyEl.hidden = rows.length > 0;

  // The read-only sentence, in the slot every other screen puts it in.
  const summary = await opened.client.summary();
  if (summary.ok) {
    setReadOnly(doc, String(summary.result.role ?? ""), "declares the types");
    // The count belongs beside the key, and the summary is the one call
    // that has it: a relation type with no edges reads differently from
    // one with four hundred.
    const listed = (Array.isArray(summary.result.relation_types) ? summary.result.relation_types : [])
      .find((entry) => entry.key === type.key);
    if (listed) {
      say(metaEl, type.key + " · " + countLabel(Number(listed.relation_count ?? 0), "relation", "relations"));
    }
  }

  return opened;
}

if (globalThis.document && globalThis.document.getElementById("relation-type-name")) {
  const opened = await openGame({ destination: DESTINATION_CATALOGUE });
  if (opened !== null) await relationTypePage(opened);
}

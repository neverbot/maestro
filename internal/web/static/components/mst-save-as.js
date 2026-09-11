// "Save as": the only view a human alone can make.
//
// **A human cannot write a query in this product, and that is a
// decision.** The view query language was written for agents, it has no
// builder, and building one is its own sub-project rather than something
// smuggled into an interface task. So this dialog is the one exception,
// and it is exactly as narrow as the exception it was granted: it copies
// the open view's query document **unchanged** under a new key and lets
// a designer change the renderer it is drawn with, that renderer's
// knobs, and the values the copy opens with. No stage of the query is
// editable here — not the seed sets, not the traversal, not the
// projection, not the limits — because the moment this dialog edits a
// query it *is* a query builder, with none of a query builder's design.
//
// The query therefore crosses the wire by reference and is never
// rebuilt: client.js's saveViewAs hands `source.query` straight to
// JSON.stringify, and save_as_test.mjs asserts the bytes on the wire are
// the source's own. Nothing in this file so much as reads a stage of it.
//
// **What it must be honest about.** A duplicate that is also wrong is a
// duplicate, not a repair. A designer who opens this dialog because a
// view answers the wrong question will get a second view answering the
// same wrong question in a different shape, so the dialog says the query
// is copied unchanged, and says where a query is changed: by asking an
// agent, over views.upsert.
//
// **The "defaults" are a binding, not a rewrite of the declarations, and
// this is a correction to the plan.** A query's declared parameters —
// their keys, their types and their defaults — live *inside the query
// document*. Editing a default is editing the document, and the same
// task requires the document be copied byte for byte; both cannot hold.
// What a designer can be given without touching the document is the
// binding the copy **opens with**, which this product already carries in
// the address (`?p.x=…`, client.js's PARAM_PREFIX): the dialog hands
// back a link to the new view with those values bound. So the dialog
// says that too, rather than letting a designer believe they have
// changed what the view will do for the next person who opens it bare.
//
// **The renderer list is the server's.** It is read over
// client.renderers() from internal/views' own catalogue — names, knobs,
// kinds and, for an enum, the admitted spellings — because a list
// spelled here would be a copy of that table with a date on it, in a
// language no Go test reads. The one thing the catalogue cannot supply
// is what a knob does to the *picture*: its `Doc` describes a contract
// for an agent (internal/views/renderers.go says so in its own header),
// and the sentence a designer needs beside a control is render/*.js's
// CONTROLS tooltip. Both are shown, and neither restates the other.
//
// It is not a LitElement, for mst-ground.js's reason: every string on
// this panel is either a game string, a designer's own typing or the
// server's sentence, and building with `createElement` and `textContent`
// means nothing on this path ever parses markup.

import { PARAM_PREFIX, writeParams } from "../client.js";
import { controlNamed } from "../render/controls.js";
import { CONTROLS as GRAPH_CONTROLS, RENDERER as RENDERER_GRAPH } from "../render/graph.js";
import { CONTROLS as LAYERED_CONTROLS, RENDERER as RENDERER_LAYERED } from "../render/layered.js";
import { CONTROLS as NESTED_CONTROLS, RENDERER as RENDERER_NESTED } from "../render/nested.js";
import { CONTROLS as MAP_CONTROLS, RENDERER as RENDERER_MAP } from "../render/map.js";
import { CONTROLS as TABLE_CONTROLS, RENDERER as RENDERER_TABLE } from "../render/table.js";
import { CONTROLS as TIMELINE_CONTROLS, RENDERER as RENDERER_TIMELINE } from "../render/timeline.js";
import { adoptControlStyles } from "./control-styles.js";

// The tooltips, by the renderer that draws them. Keyed off each module's
// own RENDERER export rather than off a list of six names typed here,
// so a renderer renamed in its module cannot leave a stale key behind.
export const TOOLTIPS = new Map([
  [RENDERER_GRAPH, GRAPH_CONTROLS],
  [RENDERER_LAYERED, LAYERED_CONTROLS],
  [RENDERER_NESTED, NESTED_CONTROLS],
  [RENDERER_MAP, MAP_CONTROLS],
  [RENDERER_TABLE, TABLE_CONTROLS],
  [RENDERER_TIMELINE, TIMELINE_CONTROLS],
]);

// The key rule, in internal/metamodel's own words.
//
// A key this dialog sends is a row key like every other in this product,
// and rowKeyProblems is what will judge it. It is checked here as well
// because a designer who typed a space should not spend a round trip to
// find out, and because the *copy* is the expensive half: a refusal on
// the key is the one refusal this dialog can prevent entirely.
//
// It is a second copy of a rule, which is what this repository calls
// drift, so internal/web/static_save_as_test.go pins all three sentences
// and the pattern to internal/metamodel/keys.go and fails if either
// side moves.
export const KEY_MAX = 64;
export const KEY_PATTERN = /^[A-Za-z0-9][A-Za-z0-9_-]*$/;
export const KEY_REQUIRED = "is required";
export const KEY_TOO_LONG = `must be at most ${KEY_MAX} characters`;
export const KEY_SHAPE = "must be letters, digits, underscores or hyphens, starting with a letter or a digit";

// The three sentences the dialog says for itself. None of them is a
// refusal: a refusal's words belong to whoever refused.
export const NOTE_QUERY_COPIED =
  "The query is copied unchanged. This dialog changes how the answer is " +
  "drawn and never what is asked, so a copy of a view that answers the " +
  "wrong question answers the same wrong question.";
export const NOTE_ASK_AN_AGENT =
  "To change what a view asks, ask an agent to write it: the query " +
  "language has no builder here, and views.upsert is where a document is " +
  "edited.";
export const NOTE_DEFAULTS_ARE_A_BINDING =
  "Values set here are the ones the copy opens with, carried in its " +
  "address. The query's own declared defaults are copied unchanged, so " +
  "anyone opening the new view without those values in the address gets " +
  "the defaults the query declares.";

export const HEADING = "Save this view under a new key";
export const LABEL_KEY = "New key";
export const LABEL_RENDERER = "Renderer";
export const LABEL_OPEN = "Save as…";
export const LABEL_SAVE = "Save the copy";
export const LABEL_CANCEL = "Cancel";
export const LABEL_OPEN_COPY = "Open the copy";

export const ACTION_OPEN = "save-as-open";
export const ACTION_SAVE = "save-as-save";
export const ACTION_CANCEL = "save-as-cancel";

export const CLASS_SAVE_AS = "save-as";
export const CLASS_NOTE = "note";
export const CLASS_BAND = "band";
export const CLASS_TOOLTIP = "tooltip";
export const CLASS_PENDING = "pending";

// FIELD_RENDERER and FIELD_PARAM are the `data-field` values the page's
// delegated input handler dispatches on, so a control is asked for by
// identity rather than by matching a label the same file renders.
export const FIELD_KEY = "key";
export const FIELD_RENDERER = "renderer";
export const FIELD_PARAM = "param";
export const FIELD_DEFAULT = "default";

// The panel's stylesheet, as a string and adopted as a constructible
// sheet rather than appended as a `<style>` element, for mst-canvas.js's
// reason: a `<style>` built in script is inline style to this product's
// Content-Security-Policy, and the browser refuses it silently. It names
// tokens and never colours, so both themes are the stylesheet's
// business.
export const SAVE_AS_CSS = `
:host { display: block; }
.save-as { max-width: 46rem; margin: 0 0 1rem; color: var(--ink); font-family: var(--sans); }
.save-as h2 { font-size: 1.05rem; margin: 0 0 0.5rem; }
.save-as label { display: block; margin: 0.6rem 0; font-size: 0.9rem; }
/* Layout only. What these controls *look* like is stated once in
   components/control-styles.js, which this root adopts before this
   sheet; restating "font: inherit" here would be a second statement of
   it. (No backticks in this comment: it lives inside a template
   literal, and one closed it.) */
.save-as input, .save-as select { display: block; margin-top: 0.2rem; }
.save-as button { margin: 0.4rem 0.4rem 0 0; }
.save-as .note { max-width: 60ch; margin: 0.4rem 0; color: var(--muted); font-size: 0.85em; }
.save-as .tooltip { max-width: 60ch; margin: 0.2rem 0 0; color: var(--muted); font-size: 0.8em; }
.save-as .band { margin: 0.6rem 0 0; color: var(--danger); font-size: 0.9em; }
/* In flight, worn by anything with a write outstanding — the canvas's
   own treatment, for the same reason: a spinner is a thing to look at
   instead of the panel a designer is still reading. */
.save-as.pending { opacity: 0.55; }
`;

// adoptSaveAsStyles is adoptCanvasStyles for this panel. It is a second
// copy of six lines rather than an import because the two components
// share no other seam and mst-canvas.js is 48 kB of drag layer this
// dialog has no business loading.
export function adoptSaveAsStyles(shadow) {
  if (!shadow || !Array.isArray(shadow.adoptedStyleSheets)) return null;
  if (typeof CSSStyleSheet !== "function") return null;
  let sheet;
  try {
    sheet = new CSSStyleSheet();
    sheet.replaceSync(SAVE_AS_CSS);
  } catch {
    return null;
  }
  shadow.adoptedStyleSheets = [...shadow.adoptedStyleSheets, sheet];
  return sheet;
}

export class MstSaveAs extends HTMLElement {
  constructor(options = {}) {
    super();
    this.doc = options.document || this.ownerDocument || globalThis.document;
    this.client = options.client || null;
    this.slug = options.slug || "";
    // The row this is a copy of. It is held whole and read for three
    // fields — its query, its renderer and its parameters — and the
    // query is never read *into* anything.
    this.row = options.row || null;
    // The catalogue, once the server has answered. Null until then, and
    // the renderer chooser is not offered before it arrives: a chooser
    // with no options is worse than a chooser that has not appeared.
    this.catalogue = null;
    this.open = false;
    this.pending = false;
    this.band = null;
    this.saved = null;
    this.key = "";
    this.renderer = this.row ? String(this.row.renderer || "") : "";
    this.params = copyParams(this.row);
    // The opening binding, as text, one entry per parameter the query
    // declares. It starts at whatever the *source* view is bound to
    // right now, which is the binding the designer is looking at.
    this.bindings = { ...(options.params || {}) };
    this.root = this.doc.createElement("div");
    const shadow = this.attachShadow({ mode: "open" });
    adoptControlStyles(shadow);
    adoptSaveAsStyles(shadow);
    if (shadow && typeof shadow.appendChild === "function") shadow.appendChild(this.root);
    this.render();
  }

  // load reads the catalogue. One call, on the first opening: the
  // catalogue is compiled into the server and cannot change under a
  // designer mid-dialog.
  async load() {
    if (this.catalogue !== null) return this.catalogue;
    if (!this.client || typeof this.client.renderers !== "function") return null;
    const answer = await this.client.renderers();
    const body = answer && answer.ok === false ? null : answer && answer.result ? answer.result : answer;
    const list = body && Array.isArray(body.renderers) ? body.renderers : null;
    if (list === null) {
      // A catalogue that did not arrive is said, not guessed at. The
      // alternative is a chooser offering a list this file invented,
      // which is the one thing this dialog may not do.
      this.band = answer && answer.ok === false ? answer.error.message : "";
      this.render();
      return null;
    }
    this.catalogue = list;
    this.render();
    return this.catalogue;
  }

  // show opens the dialog, reading the catalogue first.
  async show() {
    this.open = true;
    this.band = null;
    this.saved = null;
    this.render();
    await this.load();
    return this.open;
  }

  hide() {
    this.open = false;
    this.band = null;
    this.render();
    return this.open;
  }

  // --- What a designer may change ------------------------------------

  setKey(key) {
    this.key = typeof key === "string" ? key : "";
    this.band = null;
    this.render();
    return this.key;
  }

  // chooseRenderer switches the drawing. The parameters go back to the
  // source's only when the source's renderer is chosen again: a knob is
  // named per renderer, and carrying `graph`'s `cluster_by` onto a
  // `timeline` would compose a document views.upsert refuses.
  chooseRenderer(name) {
    this.renderer = typeof name === "string" ? name : "";
    this.params = this.renderer === String(this.row && this.row.renderer) ? copyParams(this.row) : {};
    this.band = null;
    this.render();
    return this.renderer;
  }

  setParam(name, text) {
    const entry = this.paramNamed(name);
    if (!entry) return null;
    const value = valueOf(entry.kind, text);
    if (value === null) delete this.params[name];
    else this.params[name] = value;
    this.band = null;
    this.render();
    return this.params[name] ?? null;
  }

  setBinding(key, text) {
    if (typeof text !== "string" || text === "") delete this.bindings[key];
    else this.bindings[key] = text;
    this.render();
    return this.bindings[key] ?? null;
  }

  // --- What it knows ---------------------------------------------------

  // rendererNames is what the chooser offers, and it is the server's
  // list or nothing at all.
  rendererNames() {
    return (this.catalogue || []).map((entry) => String(entry.name));
  }

  rendererEntry(name) {
    for (const entry of this.catalogue || []) {
      if (entry && entry.name === (name === undefined ? this.renderer : name)) return entry;
    }
    return null;
  }

  paramsOf(name) {
    const entry = this.rendererEntry(name);
    return entry && Array.isArray(entry.params) ? entry.params : [];
  }

  paramNamed(name) {
    for (const entry of this.paramsOf()) {
      if (entry && entry.name === name) return entry;
    }
    return null;
  }

  // tooltipFor is what this knob does to the *drawing*, from the module
  // that draws it. The catalogue's own doc is a contract for an agent
  // and is shown beside this rather than instead of it.
  tooltipFor(param) {
    const control = controlNamed(TOOLTIPS.get(this.renderer) || [], param);
    return control ? control.tooltip : "";
  }

  // declarations is the query's declared parameters — read for their
  // keys alone, so that a designer can bind them. Nothing here writes
  // one back.
  declarations() {
    const query = this.row && typeof this.row === "object" ? this.row.query : null;
    if (!query || typeof query !== "object") return [];
    return Array.isArray(query.params) ? query.params : [];
  }

  // keyProblem is internal/metamodel's rowKeyProblems, applied here so a
  // designer is not charged a round trip for a typed space. At most one
  // problem, in the same order and the same words the server uses.
  keyProblem() {
    if (this.key === "") return KEY_REQUIRED;
    if (this.key.length > KEY_MAX) return KEY_TOO_LONG;
    if (!KEY_PATTERN.test(this.key)) return KEY_SHAPE;
    return null;
  }

  // href is the address the copy opens at, with the binding a designer
  // chose. writeParams is client.js's, asked rather than repeated: the
  // bar, the link and the address agree by construction or not at all.
  href(key) {
    const bound = writeParams(this.bindings, "");
    return "/g/" + encodeURIComponent(this.slug) + "/v/" + encodeURIComponent(key) + (bound === "" ? "" : "?" + bound);
  }

  // --- The write -------------------------------------------------------

  // save is one views.upsert, and the only one this dialog makes.
  //
  // An illegal key never leaves the browser, which is the point of
  // keyProblem: the whole request would be spent to be told something
  // this file already knows. Everything else is the server's, verbatim —
  // a key already taken comes back as its own refusal and is shown
  // exactly as it arrived.
  async save() {
    const problem = this.keyProblem();
    if (problem !== null) {
      this.band = problem;
      this.render();
      return null;
    }
    this.band = null;
    this.pending = true;
    this.render();
    let answer;
    try {
      answer = await this.client.saveViewAs(this.row, {
        key: this.key,
        renderer: this.renderer,
        rendererParams: this.params,
      });
    } finally {
      this.pending = false;
    }
    if (answer && answer.ok) this.saved = { key: this.key, href: this.href(this.key) };
    else if (answer) this.band = answer.error.message;
    this.render();
    return answer;
  }

  // --- The panel -------------------------------------------------------

  render() {
    const root = this.root;
    while (root.childNodes.length > 0) root.removeChild(root.childNodes[root.childNodes.length - 1]);
    root.setAttribute("class", this.pending ? CLASS_SAVE_AS + " " + CLASS_PENDING : CLASS_SAVE_AS);

    if (!this.open) {
      // A ghost, not the primary button. "Save as" is the least likely
      // thing a person came to this screen to do — they came to read the
      // picture — and it was rendering as the one ink-filled control on
      // the page, above the drawing it is about.
      const opener = this.button(ACTION_OPEN, LABEL_OPEN);
      opener.className = "ghost";
      root.appendChild(opener);
      return root;
    }

    const heading = this.doc.createElement("h2");
    heading.textContent = HEADING;
    root.appendChild(heading);

    // The two sentences a designer needs *before* they fill anything in,
    // for the reason mst-ground.js states its three facts before a file
    // is chosen: a precondition read afterwards has spent their time to
    // say something it knew all along.
    for (const note of [NOTE_QUERY_COPIED, NOTE_ASK_AN_AGENT]) {
      root.appendChild(this.note(note));
    }

    root.appendChild(this.field(FIELD_KEY, LABEL_KEY, this.key));

    if (this.catalogue !== null) {
      root.appendChild(this.chooser());
      for (const param of this.paramsOf()) {
        const value = this.params[param.name];
        root.appendChild(
          this.control(param, value === undefined || value === null ? "" : String(value)),
        );
      }
    }

    const declarations = this.declarations();
    if (declarations.length > 0) {
      root.appendChild(this.note(NOTE_DEFAULTS_ARE_A_BINDING));
      for (const declared of declarations) {
        const key = String(declared && declared.key ? declared.key : "");
        if (key === "") continue;
        const element = this.field(FIELD_DEFAULT, PARAM_PREFIX + key, this.bindings[key] ?? "");
        element.setAttribute("data-param", key);
        root.appendChild(element);
      }
    }

    root.appendChild(this.button(ACTION_SAVE, LABEL_SAVE));
    const cancel = this.button(ACTION_CANCEL, LABEL_CANCEL);
    cancel.className = "ghost";
    root.appendChild(cancel);

    if (this.band) {
      const band = this.doc.createElement("p");
      band.setAttribute("class", CLASS_BAND);
      band.textContent = this.band;
      root.appendChild(band);
    }

    if (this.saved) {
      const link = this.doc.createElement("a");
      link.setAttribute("href", this.saved.href);
      link.textContent = LABEL_OPEN_COPY;
      root.appendChild(link);
    }
    return root;
  }

  chooser() {
    const label = this.doc.createElement("label");
    label.textContent = LABEL_RENDERER;
    const select = this.doc.createElement("select");
    select.setAttribute("data-field", FIELD_RENDERER);
    for (const name of this.rendererNames()) {
      const option = this.doc.createElement("option");
      option.setAttribute("value", name);
      if (name === this.renderer) option.setAttribute("selected", "selected");
      option.textContent = name;
      select.appendChild(option);
    }
    label.appendChild(select);
    return label;
  }

  // control is one knob: the catalogue's kind decides the element, the
  // catalogue's spellings fill an enum, and the module's tooltip says
  // what it does to the picture.
  control(param, value) {
    const label = this.doc.createElement("label");
    label.textContent = param.name + (param.required === true ? " (required)" : "");
    let input;
    if (Array.isArray(param.values) && param.values.length > 0) {
      input = this.doc.createElement("select");
      // An unset optional knob is a real state and gets a real option:
      // a select whose first entry is a value would silently assign one.
      const blank = this.doc.createElement("option");
      blank.setAttribute("value", "");
      blank.textContent = "";
      input.appendChild(blank);
      for (const spelling of param.values) {
        const option = this.doc.createElement("option");
        option.setAttribute("value", String(spelling));
        if (String(spelling) === value) option.setAttribute("selected", "selected");
        option.textContent = String(spelling);
        input.appendChild(option);
      }
    } else {
      input = this.doc.createElement("input");
      input.setAttribute("type", param.kind === "bool" ? "checkbox" : "text");
      if (param.kind === "bool") {
        if (value === "true") input.setAttribute("checked", "checked");
      } else {
        input.setAttribute("value", value);
      }
    }
    input.setAttribute("data-field", FIELD_PARAM);
    input.setAttribute("data-param", String(param.name));
    label.appendChild(input);

    const tooltip = this.doc.createElement("p");
    tooltip.setAttribute("class", CLASS_TOOLTIP);
    // Both sentences: what the knob does to the drawing, and what the
    // catalogue says a query must produce for it. Joined by a dash and
    // not by a space — the catalogue's line is a phrase written for an
    // agent and starts lowercase, so run together the two read as one
    // ungrammatical sentence. Seen on screen, on the `table` renderer's
    // four knobs.
    tooltip.textContent = [this.tooltipFor(param.name), String(param.doc || "")]
      .filter((sentence) => sentence !== "")
      .join(" — ");
    label.appendChild(tooltip);
    return label;
  }

  field(field, text, value) {
    const label = this.doc.createElement("label");
    label.textContent = text;
    const input = this.doc.createElement("input");
    input.setAttribute("type", "text");
    input.setAttribute("data-field", field);
    input.setAttribute("value", typeof value === "string" ? value : "");
    label.appendChild(input);
    return label;
  }

  note(text) {
    const paragraph = this.doc.createElement("p");
    paragraph.setAttribute("class", CLASS_NOTE);
    paragraph.textContent = text;
    return paragraph;
  }

  button(action, label) {
    const button = this.doc.createElement("button");
    button.setAttribute("type", "button");
    button.setAttribute("data-action", action);
    button.textContent = label;
    return button;
  }
}

// copyParams is the source view's renderer parameters, copied rather
// than aliased: a dialog that edited the row it is a copy *of* would
// change the picture behind it before anything was saved.
function copyParams(row) {
  const from = row && typeof row === "object" ? row.renderer_params : null;
  return from && typeof from === "object" ? { ...from } : {};
}

// valueOf turns what a control holds into what the catalogue's kind
// says the value is. An empty string is *unset* and never a value —
// deleting the key is what "this knob is off" means to views.upsert,
// and sending "" would be sending a value of the wrong type.
//
// The kinds are internal/views' own spellings, mirrored in
// render/controls.js. Everything that is not a boolean, a number or a
// list is text on the wire, which is what those kinds are: a slot name,
// a field key, a relation type key, a column reference.
export function valueOf(kind, text) {
  const raw = typeof text === "string" ? text : "";
  if (kind === "bool") return raw === "true" || raw === "on" ? true : raw === "false" ? false : null;
  if (raw === "") return null;
  if (kind === "number" || kind === "count") {
    const number = Number(raw);
    return Number.isFinite(number) ? number : null;
  }
  if (kind === "columns") {
    const list = raw
      .split(",")
      .map((part) => part.trim())
      .filter((part) => part !== "");
    return list.length > 0 ? list : null;
  }
  return raw;
}

customElements.define("mst-save-as", MstSaveAs);

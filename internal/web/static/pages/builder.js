// The query builder: a sentence in clauses, and the document it writes.
//
// **The decision this whole screen rests on** is in
// `.superpowers/specs/2026-09-11-query-builder-design.md` §2: the
// builder is closer to the sentence a designer says out loud than to the
// document a query is. The example the language was designed around is
// one sentence —
//
//   *the quests a Mage can reach between level 20 and 30, coloured by
//   zone*
//
// — and read top to bottom the clauses here are exactly that. A form
// that mirrored the document field by field would be the JSON with boxes
// around it: a person can operate that and cannot think in it.
//
// Three properties follow, and none of them is decoration:
//
//   - **A clause is optional and says so by being absent**, not by being
//     an empty box. Adding one is a single affordance at the bottom.
//   - **A value control is a picker over the game's own vocabulary**,
//     never a free-text key: a person choosing "available to" from a
//     list cannot misspell `available_to`, and a misspelling is the
//     commonest way a hand-written query fails.
//   - **The words between the controls are the product's**, not the
//     model's: "through" and "inwards", never `via` and
//     `"direction": "in"`.
//
// It **generates and does not edit** (§4): this page composes a new
// query and stores exactly what it emitted. Opening a stored document
// is a different thing and is guarded by `roundTrips` in query/compose.js.

import {
  DESTINATION_VIEWS,
  expired,
  gameURL,
  openGame,
  say,
  setBreadcrumb,
  setReadOnly,
  ROLE_VIEWER,
  viewsURL,
  viewURL,
} from "./page.js";
import {
  CLAUSE_DRAW,
  CLAUSE_FOLLOW,
  CLAUSE_FROM,
  CLAUSE_WHERE,
  TOO_MUCH,
  compose,
  decompose,
  roundTrips,
} from "../query/compose.js";
import { MstPicker, CHOOSE_EVENT, optionsFrom } from "../components/mst-picker.js";
import { entityTypeOptions, fieldOptions, relationTypeOptions } from "../components/pickers.js";
import { goToLogin } from "../app.js";

// The boundary, said once and on screen. §3: a builder that covers the
// simple half of the language and *hides* the other half is worse than
// one that covers it and says so.
export const BOUNDARY =
  "A question with two branches, a depth range, or a parameter is written as a document. " +
  "Ask an agent for it.";

// The words this screen puts between the controls. They are the
// product's own sentence, and they are constants so the harness asks for
// them by identity rather than by matching prose.
export const WORD_FROM = "Start from every";
export const WORD_WHERE = "Narrow to where";
export const WORD_FOLLOW = "Follow through";
export const WORD_DIRECTION = "going";
export const WORD_DEPTH = "up to";
export const WORD_STEPS = "steps";
export const WORD_DRAW = "Draw as a";
export const WORD_COLOUR = "coloured by";
export const ADD_WHERE = "and only where…";
export const ADD_FOLLOW = "and then follow…";
export const REMOVE_LABEL = "Remove";

// The three directions, in the product's words rather than the
// language's. The key is what reaches the document.
export const DIRECTIONS = [
  { key: "out", label: "outwards" },
  { key: "in", label: "inwards" },
  { key: "both", label: "either way" },
];

// The operators a person can compose, in the words a sentence uses. The
// language has more (internal/views/predicate.go); these are the ones
// that read as a clause, and the rest are what §3's boundary sentence is
// about.
export const OPERATORS = [
  { key: "eq", label: "is" },
  { key: "neq", label: "is not" },
  { key: "gte", label: "is at least" },
  { key: "lte", label: "is at most" },
  { key: "gt", label: "is more than" },
  { key: "lt", label: "is less than" },
  { key: "contains", label: "contains" },
  { key: "starts_with", label: "starts with" },
  { key: "exists", label: "is set" },
  { key: "empty", label: "is empty" },
];

// The two operators that take no value: a clause reading "level is set"
// with an empty box beside it is a control asking for something it will
// throw away.
export const VALUELESS = new Set(["exists", "empty"]);

// A clause's id is only ever a handle for the pointer map, so it is a
// counter rather than anything meaningful: two clauses of the same kind
// on one stack are two lines, and nothing else about them differs.
let sequence = 0;
export function nextId() {
  sequence += 1;
  return "c" + sequence;
}

// stackOf is the clause list a fresh builder starts with: one Start
// line, because a query starts from something, and nothing else. Every
// other clause is added by the person.
export function stackOf() {
  return [
    { kind: CLAUSE_FROM, id: nextId(), type: "" },
    // The Draw line is part of the sentence rather than a setting
    // underneath it: a view is a question *and* how it is drawn, and a
    // saved view with no renderer is not storable at all.
    { kind: CLAUSE_DRAW, id: nextId(), renderer: "" },
  ];
}

// documentText is what the panel beside the sentence shows: the document
// the stack currently writes, pretty-printed.
//
// Two spaces and not four: the panel is beside the sentence, not instead
// of it, and a document that needed its own scrollbar at eight clauses
// would be the thing being read.
export function documentText(stack) {
  return JSON.stringify(compose(stack).document, null, 2);
}

// saveProblems is what this page refuses on its own, before a round
// trip: the three things a stored view needs that are not part of the
// query document at all.
//
// **A refusal the page can make is a refusal the server should never
// have to.** The first version of this screen let a save go out with no
// renderer and showed the server's answer — a correct sentence about a
// field the person had never been asked for.
export function saveProblems(stack, name, key) {
  const draw = stack.find((clause) => clause.kind === CLAUSE_DRAW) ?? {};
  const problems = [];
  if (!draw.renderer) problems.push("Draw needs a way to draw it.");
  if (String(name ?? "").trim() === "") problems.push("A view needs a name.");
  if (String(key ?? "").trim() === "") problems.push("A view needs an address.");
  return problems;
}

// clauseOfPointer maps a diagnostic's JSON pointer back to the clause
// that wrote it, through the map `compose` returns.
//
// **This is the whole reason the emitter returns a pointer map** (§5):
// `views.validate` answers with a pointer, and the only thing that can
// turn that pointer into the line of the sentence that produced it is
// the mapping made while emitting. The longest matching prefix wins, so
// a pointer at `/from/0/where/all/1/value` finds the condition rather
// than the Start clause it sits under.
export function clauseOfPointer(pointers, pointer) {
  const path = String(pointer ?? "");
  if (path === "") return "";
  let best = "";
  let bestAt = "";
  for (const [control, at] of pointers) {
    if (!path.startsWith(at)) continue;
    if (at.length <= bestAt.length && best !== "") continue;
    bestAt = at;
    best = control.split(".")[0];
  }
  return best;
}

// STARTED_FROM is what the page says when it opened from a stored view,
// and it says the whole of §4 in one line: the builder generates and
// never edits, so saving writes a **new** view and the one it started
// from is not touched.
export function startedFrom(name) {
  return "This starts from " + name + ". Saving writes a new view; " + name + " is not changed.";
}

// openedFrom turns a stored view into a clause stack, or answers null.
//
// **Two checks and not one.** `decompose` refuses every shape the
// builder has no line for, and `roundTrips` refuses a document that
// would come back different — a field the builder does not know, a
// spelling it would rewrite. The second is the one that keeps this safe
// as the language grows: a clause the builder has not learned closes the
// door by itself rather than being dropped on the way through.
export function openedFrom(row) {
  const query = row && row.query !== undefined ? row.query : null;
  if (query === null) return null;
  if (!roundTrips(JSON.stringify(query))) return null;
  const clauses = decompose(query);
  if (clauses === null) return null;
  // The stack the page draws carries the Draw line, which is not part of
  // the query document at all: the renderer is the view's, not the
  // query's, so it is read off the row and appended here.
  const drawn = clauses.find((clause) => clause.kind === CLAUSE_DRAW)
    ?? { kind: CLAUSE_DRAW, id: nextId() };
  drawn.renderer = String(row.renderer ?? "");
  return clauses.includes(drawn) ? clauses : [...clauses, drawn];
}

export async function builderPage(opened) {
  const doc = opened.document;
  const root = doc.getElementById("clauses");
  const documentEl = doc.getElementById("builder-document");
  const errorEl = doc.getElementById("builder-error");
  const saveForm = doc.getElementById("builder-save");
  const saveButton = doc.getElementById("builder-save-button");
  const saveError = doc.getElementById("builder-save-error");
  const nameEl = doc.getElementById("builder-name");
  const keyEl = doc.getElementById("builder-key");

  if (opened.game === null) {
    say(errorEl, opened.failure ?? "You may not have access to this game, or it no longer exists.");
    return opened;
  }
  setBreadcrumb(doc, [
    { label: opened.game.name, href: gameURL(opened.slug) },
    { label: DESTINATION_VIEWS, href: viewsURL(opened.slug) },
    { label: "New view" },
  ]);
  doc.title = "New view · Maestro";
  say(doc.getElementById("builder-boundary"), BOUNDARY);

  const summary = await opened.client.summary();
  if (summary.ok && String(summary.result.role ?? "") === ROLE_VIEWER) {
    // A viewer is told, rather than handed a builder whose save the
    // server will refuse.
    setReadOnly(doc, summary.result.role, "saves a view");
    if (saveForm) saveForm.hidden = true;
  }

  // The game's own vocabulary, fetched once: three listings that are
  // small by construction, and every picker on this page is a choice
  // from one of them.
  const [types, relationTypes] = await Promise.all([
    entityTypeOptions(opened.client),
    relationTypeOptions(opened.client, { none: true, noneLabel: "any connection" }),
  ]);

  // **Opened from a stored view, when that view round-trips.** The
  // address carries the key rather than the document: a query in a query
  // string is a document a person can edit in the URL bar, and this page
  // is the one place in the product that must be sure what it is
  // composing from.
  const from = String(new URLSearchParams(opened.location.search ?? "").get("from") ?? "");
  let opening = null;
  if (from !== "") {
    const source = await opened.client.readView(from);
    if (source.ok) {
      const clauses = openedFrom(source.result);
      if (clauses === null) {
        // The spike's own sentence. It is said here and not only on the
        // view page, because a person can reach this address by hand.
        say(errorEl, TOO_MUCH);
      } else {
        opening = { clauses, row: source.result };
      }
    } else if (expired(source)) {
      goToLogin();
      return opened;
    } else {
      say(errorEl, source.error.message);
    }
  }

  const state = {
    stack: opening === null ? stackOf() : opening.clauses,
    pointers: new Map(),
    valid: false,
    reason: "",
  };

  // The renderers the server offers, by their own names. A builder that
  // hard-coded "graph" would be a second list of renderers to keep.
  const renderers = await opened.client.renderers();
  const rendererOptions = renderers.ok
    ? optionsFrom((renderers.result.renderers ?? []).map((row) => ({ key: row.name, label: row.name })))
    : [];

  // loadFields refreshes the one vocabulary that depends on a choice:
  // the fields of the type the Start line names.
  const loadFields = async (typeKey) => {
    state.fields = await fieldOptions(opened.client, typeKey, { none: true, noneLabel: "nothing" });
    redraw();
  };

  const redraw = () => {
    paint(doc, root, state, {
      types, relationTypes, rendererOptions, client: opened.client, redraw, validate, loadFields,
    });
    const built = compose(state.stack);
    state.pointers = built.pointers;
    say(documentEl, JSON.stringify(built.document, null, 2));
    // A structural problem is the builder's own to report, and it is
    // reported before a round trip: a Narrow line with nothing above it
    // is not a question for the server.
    const problem = built.problems[0] ?? null;
    state.reason = problem ? problem.message : state.reason;
    if (problem) state.valid = false;
    // **The builder's own problems land where the server's do**: on the
    // clause. `compose` names the clause each problem came from, so a
    // Follow line with no connection says so on that line — and the page
    // keeps the line at the bottom for the one problem that belongs to
    // no clause at all ("a query starts from something", which is about
    // a stack with no Start line in it).
    if (problem && problem.clause !== "") {
      markClause(doc, problem.clause, problem.message);
      say(errorEl, "");
    } else {
      markClause(doc, "", "");
      say(errorEl, problem ? problem.message : "");
    }
    settleSave();
  };

  const settleSave = () => {
    if (!saveButton) return;
    const mine = saveProblems(state.stack, nameEl ? nameEl.value : "", keyEl ? keyEl.value : "");
    if (mine.length > 0) {
      saveButton.disabled = true;
      say(saveError, mine[0]);
      return;
    }
    saveButton.disabled = !state.valid;
    // **A query that does not validate is never storable** (§5), and the
    // reason stands beside the button rather than arriving as a refusal
    // after a press.
    say(saveError, state.valid ? "" : state.reason);
  };

  let timer = null;
  const validate = () => {
    if (timer !== null) clearTimeout(timer);
    timer = setTimeout(async () => {
      timer = null;
      const built = compose(state.stack);
      if (built.problems.length > 0) {
        state.valid = false;
        state.reason = built.problems[0].message;
        settleSave();
        return;
      }
      const answer = await opened.client.validateView(built.document);
      if (!answer.ok) {
        if (expired(answer)) {
          goToLogin();
          return;
        }
        state.valid = false;
        // The server's own sentence, and — when it named a pointer — the
        // line of the sentence it is about.
        const field = Array.isArray(answer.error.fields) ? answer.error.fields[0] : null;
        const where = field ? clauseOfPointer(built.pointers, field.path) : "";
        state.reason = field ? field.message : answer.error.message;
        markClause(doc, where, state.reason);
        settleSave();
        return;
      }
      state.valid = answer.result.valid === true;
      state.reason = state.valid ? "" : "This query cannot be stored yet.";
      markClause(doc, "", "");
      settleSave();
    }, 250);
  };

  if (opening !== null) {
    const name = String(opening.row.name || opening.row.key || from);
    say(doc.getElementById("builder-boundary"), startedFrom(name) + " " + BOUNDARY);
    if (nameEl) nameEl.value = "Copy of " + name;
    // The address is left empty on purpose: a copy that suggested a key
    // would be one keystroke from overwriting nothing and one from
    // colliding with the view it came from, and the server refuses a
    // create at a key that exists. The person names it.
    const fromClause = state.stack.find((clause) => clause.kind === CLAUSE_FROM);
    if (fromClause && fromClause.type) void loadFields(fromClause.type);
  }

  redraw();
  validate();
  // The two boxes are part of what makes a view storable, so the button
  // answers to them as it answers to the clauses.
  for (const box of [nameEl, keyEl]) {
    if (box) box.addEventListener("input", settleSave);
  }

  if (saveForm) {
    saveForm.addEventListener("submit", async (event) => {
      if (event && typeof event.preventDefault === "function") event.preventDefault();
      const built = compose(state.stack);
      const draw = state.stack.find((clause) => clause.kind === CLAUSE_DRAW) ?? {};
      const answer = await opened.client.createView({
        key: String(keyEl ? keyEl.value : "").trim(),
        name: String(nameEl ? nameEl.value : "").trim(),
        query: built.document,
        renderer: String(draw.renderer || ""),
      });
      if (!answer.ok) {
        if (expired(answer)) {
          goToLogin();
          return;
        }
        say(saveError, answer.error.message);
        return;
      }
      // **The view it just wrote, not the list.** A person who has
      // composed a question wants to see its answer; the list is where
      // they were, not where they were going.
      opened.location.href = viewURL(opened.slug, String(keyEl ? keyEl.value : "").trim(), "");
    });
  }

  return opened;
}

// markClause puts a diagnostic under the clause that caused it, and
// clears every other one: a sentence the page wrote once and never
// cleared would tell a reader that *this* clause is wrong when it is not.
export function markClause(doc, clauseId, message) {
  const root = doc.getElementById("clauses");
  if (!root) return;
  for (const line of root.children) {
    // **Only a clause line**, which is why the attribute is the test and
    // not the position. The row of "and then follow…" buttons is a child
    // of this root too, and walking every child cleared the *last*
    // child's text: the second add button rendered as a 28px empty box,
    // every time a diagnostic was placed. Seen on screen with five
    // clauses.
    const at = line.getAttribute ? line.getAttribute("data-clause") : null;
    if (at === null) continue;
    const note = line.children[line.children.length - 1];
    if (!note) continue;
    note.textContent = at === clauseId && clauseId !== "" ? message : "";
  }
}

// paint draws the stack. It rebuilds it whole on every change, because a
// clause stack is eight lines and the alternative — patching lines in
// place — is a second model of what is on screen.
function paint(doc, root, state, deps) {
  if (!root) return;
  const lines = state.stack.map((clause) => lineFor(doc, clause, state, deps));
  const add = doc.createElement("div");
  add.className = "clause-add";
  // The two ways a stack grows, as words rather than as a plus sign: the
  // sentence continues, and the affordance says how.
  add.append(
    addButton(doc, ADD_WHERE, () => {
      addClause(state, { kind: CLAUSE_WHERE, id: nextId(), field: "", op: "eq", value: "" });
      deps.redraw();
      deps.validate();
    }),
    addButton(doc, ADD_FOLLOW, () => {
      addClause(state, { kind: CLAUSE_FOLLOW, id: nextId(), via: [], direction: "out" });
      deps.redraw();
      deps.validate();
    }),
  );
  root.replaceChildren(...lines, add);
}

// addClause puts a new line **before the Draw line**, which is the only
// ordering rule this stack has and it is a rule about the sentence: "draw
// it as a graph" is the last thing said, and a Narrow line added after it
// would read as narrowing the drawing. It matters to the document too —
// the emitter attaches a condition to the clause above it — so a stack
// that let Draw sit in the middle would be a sentence whose meaning
// depends on where somebody happened to press a button.
export function addClause(state, clause) {
  const at = state.stack.findIndex((entry) => entry.kind === CLAUSE_DRAW);
  if (at < 0) state.stack.push(clause);
  else state.stack.splice(at, 0, clause);
  return state.stack;
}

// quietButton is the same control with the treatment that says it is
// not the point of the line: taking a clause back out is a repair, not
// a step in the sentence.
function quietButton(doc, label, onClick) {
  const button = addButton(doc, label, onClick);
  button.className = "quiet";
  return button;
}

function addButton(doc, label, onClick) {
  const button = doc.createElement("button");
  button.type = "button";
  button.className = "ghost";
  button.textContent = label;
  button.addEventListener("click", onClick);
  return button;
}

// lineFor is one clause: the product's words, the controls between them,
// a way to remove it, and the place a diagnostic about it lands.
function lineFor(doc, clause, state, deps) {
  const line = doc.createElement("div");
  line.className = "clause";
  line.setAttribute("data-clause", clause.id);

  const word = (text) => {
    const span = doc.createElement("span");
    span.textContent = text;
    line.append(span);
    return span;
  };

  const changed = () => {
    deps.redraw();
    deps.validate();
  };

  if (clause.kind === CLAUSE_FROM) {
    word(WORD_FROM);
    line.append(picker(doc, {
      options: deps.types,
      chosen: clause.type,
      placeholder: "a kind of thing",
      onChoose: (option) => {
        clause.type = option.key;
        // **The fields follow the type, once.** They are fetched here
        // and held on the state rather than inside the line that draws
        // them: a fetch inside `lineFor` would run on every redraw, and
        // a redraw runs on every keystroke in a value box.
        void deps.loadFields(option.key);
        changed();
      },
    }));
  } else if (clause.kind === CLAUSE_WHERE) {
    word(WORD_WHERE);
    // The fields of whatever the Start line chose: a field picker is
    // about one type's schema, and the type is the sentence's own first
    // clause.
    line.append(picker(doc, {
      options: state.fields ?? [],
      chosen: clause.field,
      placeholder: "a field",
      onChoose: (option) => {
        clause.field = option.key;
        changed();
      },
    }));
    line.append(picker(doc, {
      options: OPERATORS,
      chosen: clause.op,
      placeholder: "is",
      onChoose: (option) => {
        clause.op = option.key;
        changed();
      },
    }));
    if (!VALUELESS.has(clause.op)) {
      const value = doc.createElement("input");
      value.type = "text";
      value.value = clause.value ?? "";
      // Sized to what goes in it, and told what that is. A 200px box
      // with nothing in it reading "level is [        ]" is a control
      // that has not said what it wants.
      value.size = 12;
      value.placeholder = "a value";
      value.setAttribute("aria-label", "the value to compare against");
      value.addEventListener("input", () => {
        clause.value = numberOrText(value.value);
        deps.redraw();
        deps.validate();
      });
      line.append(value);
    }
  } else if (clause.kind === CLAUSE_FOLLOW) {
    word(WORD_FOLLOW);
    line.append(picker(doc, {
      options: deps.relationTypes,
      chosen: Array.isArray(clause.via) ? clause.via[0] ?? "" : "",
      placeholder: "a connection",
      onChoose: (option) => {
        clause.via = option.key === "" ? [] : [option.key];
        changed();
      },
    }));
    word(WORD_DIRECTION);
    line.append(picker(doc, {
      options: DIRECTIONS,
      chosen: clause.direction,
      placeholder: "outwards",
      onChoose: (option) => {
        clause.direction = option.key;
        changed();
      },
    }));
    word(WORD_DEPTH);
    const depth = doc.createElement("input");
    depth.type = "number";
    depth.min = "1";
    // One digit, usually. It was as wide as a name.
    depth.size = 3;
    depth.value = Number.isFinite(clause.depth) ? String(clause.depth) : "1";
    depth.setAttribute("aria-label", "how many steps to follow");
    depth.addEventListener("input", () => {
      const asked = Number(depth.value);
      clause.depth = Number.isFinite(asked) && asked > 0 ? asked : undefined;
      deps.redraw();
      deps.validate();
    });
    line.append(depth);
    word(WORD_STEPS);
  } else if (clause.kind === CLAUSE_DRAW) {
    word(WORD_DRAW);
    line.append(picker(doc, {
      options: deps.rendererOptions,
      chosen: clause.renderer ?? "",
      // Not "graph": a placeholder that names a real renderer reads as a
      // choice already made, and the save was refused by the server for
      // a renderer nobody had picked.
      placeholder: "how to draw it",
      onChoose: (option) => {
        clause.renderer = option.key;
        changed();
      },
    }));
    word(WORD_COLOUR);
    line.append(picker(doc, {
      options: state.fields ?? [],
      chosen: clause.colorBy ?? "",
      placeholder: "nothing",
      onChoose: (option) => {
        clause.colorBy = option.key;
        changed();
      },
    }));
  }

  // The two clauses a view cannot be without stay: a query starts from
  // something, and a view is drawn some way. Removing either is not a
  // shorter sentence, it is no sentence — and the save would be refused
  // by the server for a reason the person could not see from here.
  if (clause.kind !== CLAUSE_FROM && clause.kind !== CLAUSE_DRAW) {
    line.append(quietButton(doc, REMOVE_LABEL, () => {
      state.stack = state.stack.filter((entry) => entry.id !== clause.id);
      deps.redraw();
      deps.validate();
    }));
  }

  // Where a diagnostic about *this* line lands (§5). It is last so the
  // sentence reads before the complaint about it.
  const note = doc.createElement("span");
  note.className = "error clause-note";
  line.append(note);
  return line;
}

// picker builds one control over a list, already wired to the one event
// the component emits.
function picker(doc, spec) {
  const control = new MstPicker({
    document: doc,
    options: spec.options,
    chosen: spec.chosen || null,
    placeholder: spec.placeholder,
  });
  control.addEventListener(CHOOSE_EVENT, (event) => spec.onChoose(event.detail));
  return control;
}

// numberOrText is the one place this page guesses at a value's type, and
// it guesses the way a person means: `20` typed into a level comparison
// is a number, and `Elwynn` is text. The server judges it against the
// field's declared type either way, and says so at the pointer this page
// can turn back into the line that wrote it.
export function numberOrText(raw) {
  const text = String(raw ?? "").trim();
  if (text === "") return "";
  const parsed = Number(text);
  return Number.isFinite(parsed) && text === String(parsed) ? parsed : text;
}

if (globalThis.document && globalThis.document.getElementById("clauses")) {
  const opened = await openGame({ destination: DESTINATION_VIEWS });
  if (opened !== null) await builderPage(opened);
}

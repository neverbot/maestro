// The query builder's emitter: a clause stack in, a query document out,
// plus the map from each control to the JSON pointer it wrote.
//
// This module is pure. It imports nothing, touches no DOM, makes no
// request, and holds no state between calls, because the two properties
// the builder rests on are both properties of a function:
//
//   1. **It generates; it does not edit** (design spec §4). The builder
//      never modifies a stored document. It opens an existing one only
//      when that document round-trips through `decompose` and `compose`
//      with nothing added, nothing dropped and no value changed — which
//      is what `roundTrips` below answers. A language addition the
//      builder has not learned closes the door by itself rather than
//      silently dropping a clause.
//   2. **Validation points at a clause, not at a page** (§5). A
//      diagnostic from `views.validate` arrives with a JSON pointer, and
//      the only thing that can turn that pointer back into the line of
//      the sentence that produced it is the mapping this emitter builds
//      while it emits. It is returned beside the document rather than
//      rebuilt afterwards for the reason the spec gives: the builder
//      must have it anyway, and a second derivation of it is a second
//      thing to drift.
//
// **On "byte for byte".** The spec says the round-trip is byte for byte.
// What is compared here is the canonical text of both sides — every
// object key sorted, deep — because whitespace and key order are not
// things the query language means, and the promise that is literally
// about bytes is the *copy* path, which never comes through this module.
// Every difference the language does mean — a field the builder does not
// know, a value it would rewrite, a clause it would drop — fails the
// comparison.
//
// The shapes it does not hold are refused by `decompose` returning null
// rather than by an approximation: `params`, branching (`traverse` whose
// `from` is not the previous set), a depth range, explicit `nodes` /
// `edges` selection, `limits`, `include_invalid`, and any key this
// module does not write. §3 lists them, and TOO_MUCH is what the builder
// says when one is met.

// The language version every document this module writes carries.
// internal/views/query.go refuses anything else.
export const V = 1;

export const CLAUSE_FROM = "from";
export const CLAUSE_WHERE = "where";
export const CLAUSE_FOLLOW = "follow";
export const CLAUSE_EDGE_WHERE = "edge-where";
export const CLAUSE_DRAW = "draw";

// What the builder says when a document says more than it can hold. One
// sentence, and it names the two ways forward rather than leaving a
// person at a closed door (§4).
export const TOO_MUCH =
  "This query says more than the builder can hold. Copy it and change how it is drawn, " +
  "or ask an agent to change it.";

// The three directions, as the language spells them. The builder's own
// words for them ("outwards", "inwards", "either way") belong to the
// clause that draws the control; this module only ever writes keys.
export const DIRECTIONS = ["out", "in", "both"];

// compose turns a clause stack into { document, pointers, problems }.
//
// `pointers` maps "<clause id>.<control>" to the JSON pointer of the
// value that control wrote. `problems` names the structural mistakes a
// picker cannot prevent and a document cannot express — a Narrow line
// with nothing above it, a stack with no Start line — so the primary
// button can be disabled with the reason beside it (§5) before a
// validate call is even worth making.
export function compose(clauses) {
  const stack = Array.isArray(clauses) ? clauses.filter(isObject) : [];
  const document = { v: V, from: [] };
  const pointers = new Map();
  const problems = [];

  const traverse = [];
  // Whether any set needs a name at all. `as` exists so a Follow line can
  // say where it starts; a stack with no Follow line writes no names,
  // because a name nothing reads is a key a stored document would not
  // have carried and the round-trip guard would refuse to reopen.
  const named = stack.some((clause) => clause.kind === CLAUSE_FOLLOW);
  // Every predicate holder built while walking, resolved in one pass at
  // the end: a second Narrow line on the same clause turns a leaf into an
  // `all`, and the shape is only settled once the stack has been read.
  const holders = [];
  // The drawing, held until the end so the document reads in the order
  // the language's stages do — from, traverse, project — whatever order
  // the clauses were written in. The round-trip guard is canonical and
  // does not care; the read-only panel beside the sentence does.
  let drawn = null;
  // The set the next Follow starts from. It is the previous step's name,
  // or the first selector's, which is what makes the stack linear: there
  // is no control for it, so a two-branch query cannot be expressed here
  // and is admitted rather than faked (§3).
  let lastSet = "";
  // The clause a Narrow line attaches to: the Start or Follow above it.
  let target = null;

  for (const clause of stack) {
    switch (clause.kind) {
      case CLAUSE_FROM: {
        const index = document.from.length;
        const base = "/from/" + index;
        const as = setName(clause, index);
        const selector = { type: text(clause.type) };
        if (as !== "" && (named || text(clause.as) !== "")) selector.as = as;
        if (Array.isArray(clause.keys) && clause.keys.length > 0) {
          selector.keys = clause.keys.map(text);
          pointers.set(clause.id + ".keys", base + "/keys");
        }
        document.from.push(selector);
        pointers.set(clause.id + ".type", base + "/type");
        if (selector.type === "") problems.push(problem(clause, "Start from needs a kind of thing."));
        if (index === 0 || lastSet === "") lastSet = as;
        target = { clause, selector, base, wheres: [] };
        break;
      }
      case CLAUSE_FOLLOW: {
        const index = traverse.length;
        const base = "/traverse/" + index;
        const as = "step" + (index + 1);
        const step = { from: lastSet, via: viaValue(clause.via), as };
        if (text(clause.direction) !== "") {
          step.direction = text(clause.direction);
          pointers.set(clause.id + ".direction", base + "/direction");
        }
        if (Number.isFinite(clause.depth)) {
          step.depth = Number(clause.depth);
          pointers.set(clause.id + ".depth", base + "/depth");
        }
        if (Array.isArray(clause.toType) && clause.toType.length > 0) {
          step.to_type = viaValue(clause.toType);
          pointers.set(clause.id + ".to_type", base + "/to_type");
        }
        traverse.push(step);
        pointers.set(clause.id + ".via", base + "/via");
        if (isEmptyVia(step.via)) problems.push(problem(clause, "Follow needs a kind of connection."));
        if (lastSet === "") problems.push(problem(clause, "Follow has nothing above it to start from."));
        lastSet = as;
        target = { clause, selector: step, base, wheres: [] };
        break;
      }
      case CLAUSE_WHERE:
      case CLAUSE_EDGE_WHERE: {
        const edge = clause.kind === CLAUSE_EDGE_WHERE;
        if (target === null || (edge && target.selector.via === undefined)) {
          problems.push(problem(clause, edge
            ? "A connection's condition needs a Follow line above it."
            : "A condition needs a Start or Follow line above it."));
          break;
        }
        const key = edge ? "edge_where" : "where";
        const held = holderFor(holders, target, key);
        held.leaves.push(leafOf(clause));
        // The pointer is written now and rewritten below when a second
        // condition on the same line turns the predicate into an `all`:
        // "/where" and "/where/all/0" are two different pointers to the
        // same control, and only the last one is true.
        pointPredicate(pointers, clause, target.base + "/" + key, held.leaves.length - 1, held.leaves.length);
        for (let i = 0; i < held.leaves.length - 1; i += 1) {
          pointPredicate(pointers, held.clauses[i], target.base + "/" + key, i, held.leaves.length);
        }
        held.clauses.push(clause);
        break;
      }
      case CLAUSE_DRAW: {
        const project = {};
        for (const [control, field] of [["label", "label"], ["colorBy", "color_by"], ["groupBy", "group_by"]]) {
          const value = text(clause[control]);
          if (value === "") continue;
          project[field] = value;
          pointers.set(clause.id + "." + control, "/project/" + field);
        }
        if (Array.isArray(clause.fields) && clause.fields.length > 0) {
          project.fields = clause.fields.map(text);
          pointers.set(clause.id + ".fields", "/project/fields");
        }
        if (Object.keys(project).length > 0) drawn = project;
        break;
      }
      default:
        problems.push(problem(clause, "This line is not one the builder writes."));
    }
  }

  for (const held of holders) {
    held.target[held.key] = held.leaves.length === 1 ? held.leaves[0] : { all: held.leaves };
  }

  if (document.from.length === 0) problems.push({ clause: "", message: "A query starts from something." });
  if (traverse.length > 0) document.traverse = traverse;
  if (drawn !== null) document.project = drawn;
  return { document, pointers, problems };
}

// decompose is compose's inverse, and returns **null** for anything it
// cannot hold rather than a best effort. That null is the door §4 closes:
// the builder opens a stored query only when this answers with a stack.
export function decompose(document) {
  if (!isObject(document)) return null;
  if (!allowedKeys(document, ["v", "from", "traverse", "project"])) return null;
  if (document.v !== V) return null;
  if (!Array.isArray(document.from) || document.from.length === 0) return null;

  const clauses = [];
  let id = 0;
  const next = () => "c" + (id += 1);

  const sets = [];
  for (const selector of document.from) {
    if (!isObject(selector)) return null;
    if (!allowedKeys(selector, ["type", "as", "keys", "where"])) return null;
    if (typeof selector.type !== "string" || selector.type === "") return null;
    if (selector.as !== undefined && typeof selector.as !== "string") return null;
    if (selector.keys !== undefined && !isStrings(selector.keys)) return null;
    const clause = { kind: CLAUSE_FROM, id: next(), type: selector.type };
    if (selector.as !== undefined) clause.as = selector.as;
    if (selector.keys !== undefined) clause.keys = [...selector.keys];
    clauses.push(clause);
    sets.push(selector.as ?? "");
    const wheres = whereClauses(selector.where, CLAUSE_WHERE, next);
    if (wheres === null) return null;
    clauses.push(...wheres);
  }

  // The linear chain, checked rather than assumed: a step starting from
  // anything but the set above it is a branch, and a branch is the one
  // thing §3 says out loud the builder will not pretend to hold.
  let lastSet = sets[0] ?? "";
  const steps = Array.isArray(document.traverse) ? document.traverse : [];
  if (document.traverse !== undefined && !Array.isArray(document.traverse)) return null;
  for (let i = 0; i < steps.length; i += 1) {
    const step = steps[i];
    if (!isObject(step)) return null;
    if (!allowedKeys(step, ["from", "via", "direction", "depth", "to_type", "where", "edge_where", "as"])) return null;
    if (step.from !== lastSet) return null;
    if (step.as !== "step" + (i + 1)) return null;
    const via = viaList(step.via);
    if (via === null) return null;
    const clause = { kind: CLAUSE_FOLLOW, id: next(), via };
    if (step.direction !== undefined) {
      if (!DIRECTIONS.includes(step.direction)) return null;
      clause.direction = step.direction;
    }
    if (step.depth !== undefined) {
      // A depth range is a document shape the builder does not write and
      // will not flatten: `{min, max}` is a different question from "up
      // to n steps", and answering it with the max would change the query.
      if (typeof step.depth !== "number" || !Number.isInteger(step.depth)) return null;
      clause.depth = step.depth;
    }
    if (step.to_type !== undefined) {
      const toType = viaList(step.to_type);
      if (toType === null) return null;
      clause.toType = toType;
    }
    clauses.push(clause);
    const wheres = whereClauses(step.where, CLAUSE_WHERE, next);
    if (wheres === null) return null;
    clauses.push(...wheres);
    const edgeWheres = whereClauses(step.edge_where, CLAUSE_EDGE_WHERE, next);
    if (edgeWheres === null) return null;
    clauses.push(...edgeWheres);
    lastSet = step.as;
  }

  if (document.project !== undefined) {
    const project = document.project;
    if (!isObject(project)) return null;
    if (!allowedKeys(project, ["label", "color_by", "group_by", "fields"])) return null;
    const clause = { kind: CLAUSE_DRAW, id: next() };
    for (const [control, field] of [["label", "label"], ["colorBy", "color_by"], ["groupBy", "group_by"]]) {
      if (project[field] === undefined) continue;
      // A related attribute — `{"related": {...}}` — is an attribute
      // reference the Draw clause has no control for. It is a shape, not
      // a string, and reading it as one would put "[object Object]" in a
      // picker.
      if (typeof project[field] !== "string") return null;
      clause[control] = project[field];
    }
    if (project.fields !== undefined) {
      if (!isStrings(project.fields)) return null;
      clause.fields = [...project.fields];
    }
    clauses.push(clause);
  }

  return clauses;
}

// roundTrips answers §4's question about one stored document, given its
// bytes: can the builder open this without changing it?
//
// It takes text rather than a parsed value because the bytes are what
// the promise is about and because a caller holding the parsed object
// has already lost the one thing being checked.
export function roundTrips(text) {
  let parsed;
  try {
    parsed = JSON.parse(String(text));
  } catch {
    return false;
  }
  const clauses = decompose(parsed);
  if (clauses === null) return false;
  const { document, problems } = compose(clauses);
  if (problems.length > 0) return false;
  return canonicalJSON(parsed) === canonicalJSON(document);
}

// canonicalJSON is JSON text with every object key sorted, deep, so that
// two documents differ here exactly when they differ in what the language
// means. Arrays keep their order: in this language order is meaning
// (`traverse` is a sequence, `from` is the order the sets are declared).
export function canonicalJSON(value) {
  return JSON.stringify(canonical(value));
}

function canonical(value) {
  if (Array.isArray(value)) return value.map(canonical);
  if (!isObject(value)) return value;
  const out = {};
  for (const key of Object.keys(value).sort()) out[key] = canonical(value[key]);
  return out;
}

// holderFor attaches a predicate to the selector or step a Narrow line
// sits under, and keeps its leaves so a second condition on the same line
// becomes an `all` rather than replacing the first.
function holderFor(holders, target, key) {
  let held = target.wheres.find((entry) => entry.key === key);
  if (held !== undefined) return held;
  held = { key, target: target.selector, leaves: [], clauses: [] };
  target.wheres.push(held);
  holders.push(held);
  return held;
}

function pointPredicate(pointers, clause, base, index, total) {
  const at = total === 1 ? base : base + "/all/" + index;
  pointers.set(clause.id + ".field", at + "/field");
  pointers.set(clause.id + ".op", at + "/op");
  pointers.set(clause.id + ".value", at + "/value");
}

function leafOf(clause) {
  const leaf = { field: text(clause.field), op: text(clause.op) };
  if (clause.value !== undefined) leaf.value = clause.value;
  return leaf;
}

// whereClauses is the predicate half of decompose: one leaf, or an `all`
// of leaves, and nothing else. `any` and `not` are shapes the stack has
// no line for, and a stack of Narrow lines reads as "and".
function whereClauses(predicate, kind, next) {
  if (predicate === undefined) return [];
  if (!isObject(predicate)) return null;
  const leaves = Array.isArray(predicate.all) ? predicate.all : [predicate];
  if (Array.isArray(predicate.all) && !allowedKeys(predicate, ["all"])) return null;
  const out = [];
  for (const leaf of leaves) {
    if (!isObject(leaf)) return null;
    if (!allowedKeys(leaf, ["field", "op", "value"])) return null;
    if (typeof leaf.field !== "string" || leaf.field === "") return null;
    if (typeof leaf.op !== "string" || leaf.op === "") return null;
    const clause = { kind, id: next(), field: leaf.field, op: leaf.op };
    if (leaf.value !== undefined) clause.value = leaf.value;
    out.push(clause);
  }
  return out;
}

// viaValue keeps the language's two spellings of a Strings field apart,
// because the round-trip guard compares what it emits against what was
// stored: one relation type is written as the bare string the language
// reads first, and the one-element array spelling is a document the
// builder declines to open rather than one it rewrites into the other
// spelling behind a person's back.
function viaValue(list) {
  const keys = Array.isArray(list) ? list.map(text) : [text(list)];
  return keys.length === 1 ? keys[0] : keys;
}

function viaList(value) {
  if (typeof value === "string") return value === "" ? null : [value];
  if (!isStrings(value) || value.length < 2) return null;
  return [...value];
}

function isEmptyVia(via) {
  return via === "" || (Array.isArray(via) && via.length === 0);
}

function setName(clause, index) {
  const as = text(clause.as);
  if (as !== "") return as;
  return text(clause.type) === "" ? "set" + (index + 1) : text(clause.type);
}

function problem(clause, message) {
  return { clause: String(clause.id ?? ""), message };
}

function allowedKeys(object, allowed) {
  return Object.keys(object).every((key) => allowed.includes(key));
}

function isStrings(value) {
  return Array.isArray(value) && value.every((item) => typeof item === "string");
}

function isObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function text(value) {
  return typeof value === "string" ? value : "";
}

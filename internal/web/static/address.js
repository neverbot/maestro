// An entity's address, in one function, for the whole front end.
//
// The product addresses an entity by `(type, key)` and never by its id:
// internal/views' own Position carries the pair "so a caller can
// round-trip it without ever having read the game's ids",
// internal/web/static/client.js writes positions by it, render/twin.js
// keys its rows by it, layout/engine.js makes it a graph vertex, and the
// renderers look a placement up by it.
//
// It is JSON rather than `type + "/" + key` so that a type or a key
// containing the separator cannot forge another entity's address.
//
// **It lives here, on its own, because everything that needs it must be
// able to reach it without reaching anything else.** It was
// layout/engine.js's export, which was right while the layout was the
// only caller — but engine.js imports the vendored dagre, and a renderer
// that had to load a layout engine to know what a node is called would
// put 48 kB of graph algorithm on the main thread to build a string. So
// engine.js re-exports this and every layout caller keeps its one
// import, while render/ imports the module with no dependencies.
//
// A non-string type or key reads as the empty string rather than as
// `null` or `undefined`: an address is a string a Map is keyed by, and
// two malformed nodes agreeing on `[null,null]` would be two nodes
// sharing an address.
export function addressOf(node) {
  return JSON.stringify([
    typeof node?.type === "string" ? node.type : "",
    typeof node?.key === "string" ? node.key : "",
  ]);
}

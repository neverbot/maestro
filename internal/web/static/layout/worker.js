// The layout worker. Spec §5.4: layout runs off the main thread, so a
// slow layout never freezes the page, the pan/zoom, or a drag already in
// flight.
//
// **This file holds no decisions, and that is its whole design.** It is
// the one part of this layer no Node harness can drive — a worker needs
// a browser — so everything that could be wrong lives on the other side
// of the seam, in compose.js and engine.js, which are pure functions a
// harness imports directly. What is left here is a message in, one call,
// a message out. If a future edit finds itself adding a branch to this
// file, the branch belongs in compose.js instead.
//
// It is a **module** worker (`new Worker(url, { type: "module" })`), so
// it imports the vendored dagre through engine.js with no build step —
// the second reason the no-build-step rule survives this sub-project.
// Every import in this graph is by relative path and never by bare
// specifier: an import map belongs to a *document*, and a worker has
// none, so `import … from "dagre"` here would fail to resolve at load
// and the page would see only a worker that never answered. engine.js's
// header says the same thing where the imports actually are.
//
// The budget is not enforced here. A worker cannot reliably interrupt
// its own synchronous layout, so the deadline is the page's: budget.js's
// supervisor races this worker against a timer and calls `terminate()`,
// which is the only thing that actually stops a dagre run.

import { layoutView } from "./compose.js";

self.addEventListener("message", (event) => {
  const request = event.data && typeof event.data === "object" ? event.data : {};
  // The id is echoed back untouched. The page may have asked twice — a
  // parameter changed while the first run was still going — and an
  // answer that could not be matched to its question would be an
  // arrangement drawn from a query nobody is looking at any more.
  const id = request.id;
  try {
    self.postMessage({ id, ok: true, layout: layoutView(request) });
  } catch (error) {
    // A throw is reported as a throw. It is not a timeout and must not
    // be dressed as one: budget.js's sentence explains a deadline, and
    // offering "try again with 10 seconds" for an exception offers to
    // wait longer for the same throw.
    self.postMessage({
      id,
      ok: false,
      error: error && error.message ? String(error.message) : String(error),
    });
  }
});

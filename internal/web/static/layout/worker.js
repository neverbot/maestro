// The layout worker. Spec §5.4: layout runs off the main thread, so a
// slow layout never freezes the page, the pan/zoom, or a drag already in
// flight.

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

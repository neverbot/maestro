// The layout budget: the number, the sentence generated from it, the one
// escalation it allows, and the supervisor that enforces it.
//
// Spec §5.4 and the plan's O10 decide the number and where it lives:
// **one exported constant, read by the worker's supervisor and by the
// retry button, and nowhere restated.** The sentence a designer reads is
// *generated from the constant* rather than written beside it, so that
// changing the timeout changes the prose in the same edit. A hard-coded
// "2 seconds" next to a 2000 is a lie waiting for its first tuning
// commit, and `theBudgetTerminatesAndTheGridIsAnnounced` is the guard:
// it compares the banner against `budgetSentence(LAYOUT_BUDGET_MS)`
// computed from the constant, so the two can only ever agree.
//
// **Why a supervisor lives here and not in the worker.** The worker is
// the one part of this layer a Node harness cannot drive, so it holds no
// decisions: it receives a request, calls the pure composition and posts
// the answer back (worker.js). Everything about *giving up* — when, what
// is drawn instead, what is said, and what the retry costs — is a
// decision, so it is here, expressed over an injected timer and an
// injected clock, which is what lets a two-second budget cost a
// millisecond of test time. That mirrors internal/web/static/client.js,
// whose timers are injected for the same reason.
//
// The clock is injected *and read*: `elapsedMs` is the number spec §5.4
// puts in the footer strip beside `duration_ms`, so that "the query is
// slow" and "the picture is slow" are two answers rather than one.
// internal/web/static/render/scene.js's `footerFor` already takes a
// `layoutMs`; this is what produces it.

// LAYOUT_BUDGET_MS is the first attempt's ceiling. It is a guess about a
// designer's patience and not a measurement — Task 18 records the real
// elapsed layout time for the largest view it can build, which is the
// number this one should be revisited against.
export const LAYOUT_BUDGET_MS = 2000;

// LAYOUT_RETRY_MS is the *only* escalation, and it is explicit, per view
// and never automatic: the second attempt is on the same failing input,
// so retrying on the designer's behalf would only spend ten more seconds
// arriving at the same grid.
export const LAYOUT_RETRY_MS = 10000;

// budgetSentence is the banner's whole text, from the number.
//
// It says three things and no more: that the deadline was missed, what
// is on screen instead, and the two things a designer can do about it. A
// grid presented without explanation reads as *the graph having no
// structure*, which is a false statement about the game — this sentence
// exists to stop the interface making it.
export const budgetSentence = (ms) =>
  `Layout did not finish in ${ms / 1000} seconds; nodes are arranged in a grid. ` +
  `Narrow the query, or drag what matters.`;

// The banner and the action codes, in scene.js's vocabulary: an exported
// code rather than a bare string at the call site, so a test asks for a
// band by identity and never by matching its prose.
//
// They are not in scene.js's BANNER_ORDER, and deliberately. That stack
// is built from the **envelope**, and this band is not in the envelope —
// it is a statement about this browser's last two seconds, which a
// second designer looking at the same view may never see. The canvas
// (Task 7) places it; the frame's model does not know it exists.
export const BANNER_LAYOUT_BUDGET = "layout_budget";
export const ACTION_RETRY_LAYOUT = "retry_layout";

// nextBudgetMs is the escalation rule, and it is one step.
//
// From the ordinary budget it goes to the retry budget; from the retry
// budget it goes nowhere, and answers null so the caller can offer no
// button rather than an infinite ladder of longer waits. Any other value
// — a caller that invented its own budget — also gets one step to the
// retry budget, because the ladder has exactly two rungs by
// construction and not by the caller's discipline.
export function nextBudgetMs(currentMs) {
  return currentMs === LAYOUT_RETRY_MS ? null : LAYOUT_RETRY_MS;
}

// runWithBudget runs one layout attempt against a deadline.
//
// `run` is anything thenable — in the browser it is a promise settled by
// the worker's `message` handler, in the harness it is a stub — and
// `cancel` is what stops it: `worker.terminate()`, because a module
// worker running a super-linear layout does not answer a polite request
// to stop. It is called exactly once, on the timeout path only.
//
// `fallback` produces the arrangement drawn instead. It is passed in
// rather than imported so that this function knows nothing about what a
// placement is; compose.js's `gridFallback` is what the canvas hands it.
//
// The answer is one shape in both directions, with `ok` saying which:
// a caller that has to tell them apart by looking for an absent field is
// a caller that will get it wrong once.
//
// A `run` that **rejects** rejects here, and does not become a grid: a
// worker that failed is not a worker that was slow, the sentence below
// would be a false explanation of it, and offering a longer budget for
// an exception is offering to wait longer for the same throw. The canvas
// (Task 7) owns that path.
export async function runWithBudget({
  run,
  budgetMs = LAYOUT_BUDGET_MS,
  fallback = () => [],
  cancel = () => {},
  setTimer = setTimeout,
  clearTimer = clearTimeout,
  now = () => performance.now(),
} = {}) {
  const started = now();
  let timer = null;
  let timedOut = false;

  const deadline = new Promise((resolve) => {
    timer = setTimer(() => {
      timedOut = true;
      resolve(null);
    }, budgetMs);
  });

  const settled = await Promise.race([
    Promise.resolve(typeof run === "function" ? run() : run).then((value) => ({ value })),
    deadline,
  ]);

  if (!timedOut) {
    clearTimer(timer);
    return {
      ok: true,
      layout: settled ? settled.value : null,
      elapsedMs: now() - started,
      budgetMs,
      banner: null,
      retry: null,
    };
  }

  // Past the deadline. The worker is terminated rather than left to
  // finish into a page that has stopped listening — a layout nobody
  // reads is still a core spinning — and what is drawn instead is
  // **announced**, every time, in the same sentence as the number that
  // produced it.
  cancel();
  const escalated = nextBudgetMs(budgetMs);
  return {
    ok: false,
    layout: fallback(),
    elapsedMs: now() - started,
    budgetMs,
    banner: {
      code: BANNER_LAYOUT_BUDGET,
      css: "var(--muted)",
      text: budgetSentence(budgetMs),
      rows: [],
    },
    retry: escalated === null ? null : { code: ACTION_RETRY_LAYOUT, budgetMs: escalated },
  };
}

// The layout budget: the number, the sentence generated from it, the one
// escalation it allows, and the supervisor that enforces it.
export const LAYOUT_BUDGET_MS = 2000;

// LAYOUT_RETRY_MS is the *only* escalation, and it is explicit, per view
// and never automatic: the second attempt is on the same failing input,
// so retrying on the designer's behalf would only spend ten more seconds
// arriving at the same grid.
export const LAYOUT_RETRY_MS = 10000;

// budgetSentence is the banner's whole text, from the number.
export const budgetSentence = (ms) =>
  `Layout did not finish in ${ms / 1000} seconds; nodes are arranged in a grid. ` +
  `Narrow the query, or drag what matters.`;

// The banner and the action codes, in scene.js's vocabulary: an exported
// code rather than a bare string at the call site, so a test asks for a
// band by identity and never by matching its prose.
export const BANNER_LAYOUT_BUDGET = "layout_budget";
export const ACTION_RETRY_LAYOUT = "retry_layout";

// nextBudgetMs is the escalation rule, and it is one step.
export function nextBudgetMs(currentMs) {
  return currentMs === LAYOUT_RETRY_MS ? null : LAYOUT_RETRY_MS;
}

// runWithBudget runs one layout attempt against a deadline.
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

package analysis

import "time"

// Every bound this package applies is in this file and every one of them
// is exported. That is the rule correction 24 established for
// MaxSearchQuery, carried here deliberately: **a bound a caller cannot
// read is a bound a caller trips over.** TestEveryBoundIsExported reads
// this file's own AST and fails on an unexported const in it, which is
// the guard that keeps the rule true for the constants a later task
// adds rather than only for these.
//
// **Refuse or clamp is not a matter of taste here, and the split is not
// new.** A *declared limit* above its cap is refused, naming the cap —
// `metamodel.MaxBulkItems` is the precedent and this package copies it —
// because a caller that asked for five hundred and silently got a
// hundred reads a partial answer as a complete one, and this engine's
// entire value is that a designer can trust what it says. A **page
// limit** is the documented exception and is clamped (`paging.Size`),
// because a page is explicitly one slice of an answer whose remainder
// the cursor promises. Nothing in this file is a page limit, so
// everything in this file refuses.
const (
	// DefaultMaxDepth and MaxMaxDepth bound a walk's hops.
	//
	// A depth above MaxMaxDepth is limit_exceeded naming the cap — never
	// clamped. A run that quietly walked 100 hops for a caller that
	// asked for 500 answers "nothing further is reachable" when what it
	// means is "I stopped looking", and those two are the same JSON.
	DefaultMaxDepth = 25
	MaxMaxDepth     = 100

	// DefaultMaxResults and MaxMaxResults bound findings. Same refusal,
	// for the same reason: a findings list trimmed to its cap without
	// saying so reads as the whole list of what is wrong with a game.
	DefaultMaxResults = 100
	MaxMaxResults     = 1000

	// MaxWalkRows caps the rows any single walk may return, and is not
	// caller-settable at all — so it neither refuses nor clamps a
	// caller's argument, having none to judge. It is an order of
	// magnitude above the rows a 1000-finding answer can need, and it
	// exists so a pathological graph cannot turn one analysis into an
	// outage. A walk that hits it sets `truncated`: graph.WalkCTE emits
	// LIMIT MaxRows+1 precisely so that hitting the cap is detectable
	// rather than indistinguishable from an answer that fitted exactly.
	MaxWalkRows = 50_000

	// MaxSeedKeys, MaxRouteSteps and MaxTypeKeys bound the caller's own
	// lists.
	//
	// Five hundred is metamodel.MaxBulkItems, deliberately and not
	// coincidentally: a step list and a seed list are batches, they are
	// refused rather than clamped for the reason that constant's own
	// comment gives, and a third number would be a third thing to
	// remember. MaxTypeKeys is smaller because a game with more than
	// sixty-four relation types filtered by name is not narrowing an
	// analysis, and the list is a filter rather than content.
	MaxSeedKeys   = 500
	MaxRouteSteps = 500
	MaxTypeKeys   = 64

	// MaxBlockers is how many blocking entities an unreachable finding
	// names. Five is the spec's number and it is a **readability** bound
	// and not a cost one: a reason that names forty entities is a reason
	// nobody reads. It bounds an answer this package composes rather
	// than an argument a caller sends, so there is nothing to refuse.
	MaxBlockers = 5
)

// DefaultStatementTimeout and HardStatementTimeout are this package's own
// two constants and deliberately **not** aliases of internal/views'.
//
// That they disagree with that package's 5s/15s is the point, and the
// disagreement is a conclusion rather than an inheritance —
// internal/views/events.go records the same reasoning for its gating
// constants, where the two domains happened to *agree* and said so as a
// conclusion too. A view narrows itself with a `from` selector before it
// walks anything; an analysis is defined as the walk over a whole game,
// which is the expensive shape by construction. Fifteen seconds is the
// ceiling views chose for a narrowed read; thirty is the spec's cap for
// this one.
//
// A run that exhausts the budget answers `retryable` — SQLSTATE 57014 is
// already in metamodel's retryableSQLStates — with a message naming the
// elapsed budget and the arguments that narrow the run. There is no
// `analysis_timeout` code; errors.go argues why at length.
const (
	DefaultStatementTimeout = 5 * time.Second
	HardStatementTimeout    = 30 * time.Second
)

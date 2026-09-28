package analysis

import "time"

// Every bound this package applies is in this file and every one of them
// is exported. That is the rule correction 24 established for
// MaxSearchQuery, carried here deliberately: **a bound a caller cannot
// read is a bound a caller trips over.** TestEveryBoundIsExported reads
// this file's own AST and fails on an unexported const in it, which is
// the guard that keeps the rule true for the constants a later task
// adds rather than only for these.
const (
	// DefaultMaxDepth and MaxMaxDepth bound a walk's hops.
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
const (
	DefaultStatementTimeout = 5 * time.Second
	HardStatementTimeout    = 30 * time.Second
)

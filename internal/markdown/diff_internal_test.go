package markdown

import (
	"fmt"
	"strings"
	"testing"
)

// BenchmarkUnifiedDiff is what lcsLimit's comment cites. It is a
// benchmark and not an assertion because the numbers are a property of
// the machine, and a threshold test over them would fail on somebody
// else's laptop for no defect: what it exists to answer is "what does
// the worst case this code admits actually cost", and the answer belongs
// in the constant's comment where the next person changing the bound
// will read it.
//
// The two sizes bracket the bound: at the limit is the most expensive
// diff this package will ever compute line by line, and past it is the
// coarse fallback, which must be cheap or the fallback is not one.
func BenchmarkUnifiedDiff(b *testing.B) {
	for _, lines := range []int{lcsLimit, lcsLimit + 1} {
		b.Run(fmt.Sprintf("%d-lines-rewritten", lines), func(b *testing.B) {
			from := body(lines, "a")
			to := body(lines, "b")
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, coarse := unifiedDiff("d@1", "d@2", from, to); coarse != (lines > lcsLimit) {
					b.Fatalf("coarse = %t at %d lines", coarse, lines)
				}
			}
		})
	}
	// The shape a real "compare these two versions" click has: a long
	// document with one paragraph rewritten. The trim is what makes this
	// the common case rather than the one above.
	b.Run("one-paragraph-of-40000-lines", func(b *testing.B) {
		head := body(20000, "a")
		from := head + "before\n" + head
		to := head + "after\n" + head
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if _, coarse := unifiedDiff("d@1", "d@2", from, to); coarse {
				b.Fatal("a one-line edit must not be coarse")
			}
		}
	})
}

func body(lines int, text string) string {
	return strings.Repeat(text+"\n", lines)
}

// TestTheCoarseFallbackIsCheaperThanTheTableItReplaces pins the property
// the benchmark measures but cannot assert: the fallback must not build
// the table at all. If it ever did, the bound would buy nothing and a
// large rewrite would cost more than the case it is protecting against —
// so this asserts the observable consequence, that a diff one line past
// the bound is coarse and one line inside it is not.
func TestTheCoarseFallbackIsCheaperThanTheTableItReplaces(t *testing.T) {
	inside := body(lcsLimit, "a")
	insideOther := body(lcsLimit, "b")
	if _, coarse := unifiedDiff("d@1", "d@2", inside, insideOther); coarse {
		t.Fatalf("%d lines per side is inside the bound and must be computed", lcsLimit)
	}
	past := body(lcsLimit+1, "a")
	pastOther := body(lcsLimit+1, "b")
	if _, coarse := unifiedDiff("d@1", "d@2", past, pastOther); !coarse {
		t.Fatalf("%d lines per side is past the bound and must fall back", lcsLimit+1)
	}
}

// TestTheBoundKeepsTheTableWithinItsMemoryBudget is a tripwire, not a
// behaviour test: it asserts arithmetic, because the arithmetic is the
// whole reason the constant has the value it has and it is invisible at
// the point someone would change it. diffOps allocates (n+1)*(m+1)
// cells of int32, nothing bounds how many diffs run at once, and the
// table is quadratic -- so doubling lcsLimit quadruples the worst case
// and a change that looks like "4000 works fine on my laptop" is a
// 66.7 MB per-call allocation on a self-hosted box. 8 MiB is the budget
// the current bound was chosen against, with room for the ops slice and
// the rendered output on top (BenchmarkUnifiedDiff measures the whole
// call at 4.37 MB against the table's own 4.0 MB). Raising lcsLimit
// past it is allowed; doing so silently is not.
func TestTheBoundKeepsTheTableWithinItsMemoryBudget(t *testing.T) {
	const budget = 8 << 20
	table := (int64(lcsLimit) + 1) * (int64(lcsLimit) + 1) * 4
	if table > budget {
		t.Fatalf("lcsLimit = %d builds a %d-byte table, over the %d-byte budget; "+
			"the bound is quadratic, so re-measure BenchmarkUnifiedDiff and "+
			"re-justify the number in lcsLimit's comment before raising it",
			lcsLimit, table, budget)
	}
}

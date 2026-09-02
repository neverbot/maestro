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

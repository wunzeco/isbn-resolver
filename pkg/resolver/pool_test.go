package resolver

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// scrambleDelay returns a small pseudo-random delay derived from isbn rather
// than a shared *rand.Rand, since Resolve invokes the resolve func from
// multiple worker goroutines concurrently and rand.Rand is not safe for
// concurrent use.
func scrambleDelay(isbn string) time.Duration {
	h := fnv32(isbn)
	return time.Duration(h%3) * time.Millisecond
}

func fnv32(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// TestResolvePreservesOrderUnderShuffledLatency is the headline promise of the
// worker pool (plan item "Replace the sequential loop with a bounded worker
// pool"): whichever worker finishes first, results land in the same order as
// the input, because each worker writes into its own pre-sized slot rather
// than appending.
func TestResolvePreservesOrderUnderShuffledLatency(t *testing.T) {
	isbns := make([]string, 50)
	for i := range isbns {
		isbns[i] = fmt.Sprintf("isbn-%02d", i)
	}

	resolve := func(isbn string) (*BookMetadata, string, error) {
		// Deliberately scramble completion order across workers.
		time.Sleep(scrambleDelay(isbn))
		return &BookMetadata{ISBN: isbn, Title: "Title for " + isbn}, APIOpenLibrary, nil
	}

	results := Resolve(8, isbns, resolve)

	if len(results) != len(isbns) {
		t.Fatalf("got %d results, want %d", len(results), len(isbns))
	}
	for i, r := range results {
		if r.Index != i {
			t.Errorf("results[%d].Index = %d, want %d", i, r.Index, i)
		}
		if r.ISBN != isbns[i] {
			t.Errorf("results[%d].ISBN = %q, want %q", i, r.ISBN, isbns[i])
		}
		if r.Metadata == nil || r.Metadata.ISBN != isbns[i] {
			t.Errorf("results[%d].Metadata = %+v, want ISBN %q", i, r.Metadata, isbns[i])
		}
	}
}

// TestResolveBoundsConcurrency asserts the pool never runs more than
// `concurrency` resolves at once, which is the entire point of a *bounded*
// worker pool talking to free public APIs (spec §3).
func TestResolveBoundsConcurrency(t *testing.T) {
	const concurrency = 3
	isbns := make([]string, 20)
	for i := range isbns {
		isbns[i] = fmt.Sprintf("isbn-%02d", i)
	}

	var inFlight, maxInFlight int64
	resolve := func(isbn string) (*BookMetadata, string, error) {
		cur := atomic.AddInt64(&inFlight, 1)
		for {
			max := atomic.LoadInt64(&maxInFlight)
			if cur <= max || atomic.CompareAndSwapInt64(&maxInFlight, max, cur) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		atomic.AddInt64(&inFlight, -1)
		return &BookMetadata{ISBN: isbn}, APIOpenLibrary, nil
	}

	Resolve(concurrency, isbns, resolve)

	if maxInFlight > concurrency {
		t.Errorf("observed %d concurrent resolves, want <= %d", maxInFlight, concurrency)
	}
	if maxInFlight < 2 {
		t.Errorf("observed only %d concurrent resolves, pool doesn't look parallel", maxInFlight)
	}
}

// TestResolvePropagatesErrors ensures a per-ISBN failure surfaces on its own
// Result without disrupting the rest of the batch.
func TestResolvePropagatesErrors(t *testing.T) {
	isbns := []string{"good-1", "bad", "good-2"}
	resolve := func(isbn string) (*BookMetadata, string, error) {
		if isbn == "bad" {
			return nil, "", fmt.Errorf("upstream said no")
		}
		return &BookMetadata{ISBN: isbn}, APIGoogleBooks, nil
	}

	results := Resolve(2, isbns, resolve)

	if results[1].Err == nil {
		t.Errorf("results[1].Err = nil, want an error for %q", isbns[1])
	}
	if results[0].Err != nil || results[2].Err != nil {
		t.Errorf("unexpected error on a good ISBN: %+v", results)
	}
}

// TestResolveHandlesDegenerateConcurrency covers the two edge cases that
// would otherwise deadlock or panic: no input, and a concurrency value below
// 1 (e.g. an unvalidated config's zero-value default).
func TestResolveHandlesDegenerateConcurrency(t *testing.T) {
	resolve := func(isbn string) (*BookMetadata, string, error) {
		return &BookMetadata{ISBN: isbn}, APIOpenLibrary, nil
	}

	if got := Resolve(4, nil, resolve); len(got) != 0 {
		t.Errorf("Resolve with no input returned %d results, want 0", len(got))
	}

	results := Resolve(0, []string{"a", "b"}, resolve)
	if len(results) != 2 {
		t.Fatalf("Resolve with concurrency 0 returned %d results, want 2", len(results))
	}
}

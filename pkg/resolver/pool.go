package resolver

import "sync"

// ResolveFunc resolves a single ISBN into metadata, the name of the API that
// supplied it, and an error. Both APIClient.Resolve and test fakes satisfy it,
// so Resolve below never needs to know about APIClient directly.
type ResolveFunc func(isbn string) (metadata *BookMetadata, source string, err error)

// Result is one job's outcome, carrying the index of the input it answers so
// the caller can drop it back into the right output slot.
type Result struct {
	Index    int
	ISBN     string
	Metadata *BookMetadata
	// Source names the API tier that answered, empty when none did. It rides
	// on the Result rather than on BookMetadata because it describes this
	// resolution, not the book — the same book resolved tomorrow may well come
	// from a different tier.
	Source string
	Err    error
}

// Resolve runs resolve over isbns across up to concurrency workers and
// returns one Result per input, in the same order as isbns.
//
// Concurrency is bounded because this talks to two free public APIs, not
// infrastructure the caller controls (spec §3, performance-caching.md).
// concurrency < 1 is treated as 1 rather than deadlocking on a closed jobs
// channel with no workers to drain it.
//
// Each worker writes only to the result slots of the jobs it was handed, so
// results is safe to share across goroutines without a mutex: the index sets
// each worker touches are disjoint by construction.
func Resolve(concurrency int, isbns []string, resolve ResolveFunc) []Result {
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > len(isbns) {
		concurrency = len(isbns)
	}

	results := make([]Result, len(isbns))
	jobs := make(chan int)

	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				metadata, source, err := resolve(isbns[i])
				results[i] = Result{Index: i, ISBN: isbns[i], Metadata: metadata, Source: source, Err: err}
			}
		}()
	}

	for i := range isbns {
		jobs <- i
	}
	close(jobs)

	wg.Wait()

	return results
}

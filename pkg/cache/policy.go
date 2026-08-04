package cache

import "fmt"

// Mode selects how the cache is consulted for a run. It is the single resolved
// form of the --resolve-all / --retry-failed / --no-cache flags (spec §2), so
// the resolve loop asks one question per ISBN instead of re-deriving flag
// combinations at every decision point.
type Mode int

const (
	// ModeNormal reuses every cached entry, successes *and* errors. Caching
	// failures is deliberate: known-bad ISBNs would otherwise hammer the two
	// free public APIs on every run.
	ModeNormal Mode = iota
	// ModeResolveAll ignores cached entries entirely and re-resolves every
	// input ISBN, overwriting its entry. For suspected-stale metadata.
	ModeResolveAll
	// ModeRetryFailed reuses cached successes but re-attempts entries cached
	// with status error — the light-weight fix for a run where some ISBNs
	// failed transiently.
	ModeRetryFailed
	// ModeNoCache bypasses cache reads and writes altogether, for ad hoc runs
	// that shouldn't pollute the cache.
	ModeNoCache
)

// String renders the mode for verbose output and error messages.
func (m Mode) String() string {
	switch m {
	case ModeNormal:
		return "normal"
	case ModeResolveAll:
		return "resolve-all"
	case ModeRetryFailed:
		return "retry-failed"
	case ModeNoCache:
		return "no-cache"
	default:
		return fmt.Sprintf("Mode(%d)", int(m))
	}
}

// Persists reports whether results should be written back to the cache file at
// the end of the run. Only --no-cache opts out; --resolve-all and
// --retry-failed still refresh the entries they re-resolve.
func (m Mode) Persists() bool {
	return m != ModeNoCache
}

// Counters is the tally behind the verbose "Cache: H hit, M miss, R retried"
// line. Every Lookup falls into exactly one bucket, so Hits+Misses+Retried is
// the number of ISBNs considered.
type Counters struct {
	// Hits are ISBNs answered from the cache with no network call.
	Hits int
	// Misses are ISBNs with no usable cached entry.
	Misses int
	// Retried are ISBNs that had a usable entry which the mode deliberately
	// re-resolved anyway.
	Retried int
}

// Total is the number of ISBNs the policy has been asked about.
func (c Counters) Total() int {
	return c.Hits + c.Misses + c.Retried
}

// Policy answers "do I need to resolve this ISBN?" for one run, given a cache
// and a mode, and tallies the answers for the verbose breakdown.
//
// A Policy is not safe for concurrent use; the lookup pass runs on one
// goroutine before work is handed to the worker pool.
type Policy struct {
	cache    *Cache
	mode     Mode
	counters Counters
}

// NewPolicy builds a policy over the given cache. A nil cache is valid and
// behaves as an empty one, which is what ModeNoCache passes since it never
// loads a cache file.
func NewPolicy(c *Cache, mode Mode) *Policy {
	return &Policy{cache: c, mode: mode}
}

// Mode reports the mode this policy was built with.
func (p *Policy) Mode() Mode {
	return p.mode
}

// Counters returns the tally so far.
func (p *Policy) Counters() Counters {
	return p.counters
}

// Lookup decides what to do with one ISBN key and records the decision in the
// counters. The returned entry is only meaningful when reuse is true — that is
// the cached result to drop straight into the output slot.
//
// Callers must call Lookup (or ShouldResolve, which wraps it) exactly once per
// ISBN, since each call counts.
func (p *Policy) Lookup(key string) (entry Entry, reuse bool) {
	cached, ok := p.cache.Get(key)

	// Nothing usable on disk: an absent key, or an entry a hand-edited or
	// partially-written cache file left in a state we can't honour (unknown
	// status, or a success with no metadata to reuse). Treating those as
	// misses re-resolves them and repairs the entry.
	if !ok || !usable(cached) {
		p.counters.Misses++
		return Entry{}, false
	}

	if p.reusable(cached) {
		p.counters.Hits++
		return cached, true
	}

	// A usable entry the mode chose to re-resolve anyway.
	p.counters.Retried++
	return Entry{}, false
}

// ShouldResolve reports whether the ISBN needs a network call. It is the
// discard-the-entry view of Lookup and counts identically.
func (p *Policy) ShouldResolve(key string) bool {
	_, reuse := p.Lookup(key)
	return !reuse
}

// reusable applies spec §1–§2 to a usable entry.
func (p *Policy) reusable(entry Entry) bool {
	switch p.mode {
	case ModeNormal:
		// Successes and errors alike are reused.
		return true
	case ModeRetryFailed:
		return entry.Status == StatusSuccess
	case ModeResolveAll, ModeNoCache:
		return false
	default:
		// An unrecognised mode must not silently serve stale data.
		return false
	}
}

// usable reports whether an entry carries enough information to be reused if
// the mode allows it.
func usable(entry Entry) bool {
	switch entry.Status {
	case StatusSuccess:
		return entry.Metadata != nil
	case StatusError:
		return true
	default:
		return false
	}
}

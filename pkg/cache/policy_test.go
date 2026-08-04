package cache

import (
	"testing"
	"time"

	"github.com/wunzeco/isbn-resolver/pkg/resolver"
)

const testKey = "9780134190440"

// entryState names one of the shapes a cache can be in for a given key. The
// last two are not states the tool writes itself — they are what a hand-edited,
// partially-written, or newer-format cache file can leave behind — but the
// policy is the only thing standing between them and a wrong answer being
// served as a hit, so they belong in the matrix.
type entryState string

const (
	stateAbsent        entryState = "absent"
	stateSuccess       entryState = "success"
	stateError         entryState = "error"
	stateSuccessNoMeta entryState = "success without metadata"
	stateUnknownStatus entryState = "unknown status"
)

// cacheFor builds a cache holding testKey in the requested state.
func cacheFor(state entryState) *Cache {
	c := New()
	attempt := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	switch state {
	case stateAbsent:
		// Deliberately empty.
	case stateSuccess:
		c.Set(testKey, Entry{
			Status:      StatusSuccess,
			Metadata:    &resolver.BookMetadata{ISBN: testKey, Title: "The Go Programming Language"},
			LastAttempt: attempt,
		})
	case stateError:
		c.Set(testKey, Entry{
			Status:      StatusError,
			Error:       "not found in any source",
			LastAttempt: attempt,
		})
	case stateSuccessNoMeta:
		c.Set(testKey, Entry{Status: StatusSuccess, LastAttempt: attempt})
	case stateUnknownStatus:
		c.Set(testKey, Entry{Status: Status("pending"), LastAttempt: attempt})
	}

	return c
}

// bucket names the counter a lookup lands in, so each case asserts the decision
// and the tally it produced together.
type bucket string

const (
	bucketHit     bucket = "hit"
	bucketMiss    bucket = "miss"
	bucketRetried bucket = "retried"
)

// TestPolicyLookupMatrix covers every (mode × entry state) pair, which is the
// whole of spec §1–§2: normal reuses successes *and* errors, --retry-failed
// reuses successes only, --resolve-all and --no-cache always resolve. A wrong
// cell here either hammers the free public APIs or serves stale metadata, and
// neither is visible from the tool's output.
func TestPolicyLookupMatrix(t *testing.T) {
	modes := []Mode{ModeNormal, ModeResolveAll, ModeRetryFailed, ModeNoCache}
	states := []entryState{stateAbsent, stateSuccess, stateError, stateSuccessNoMeta, stateUnknownStatus}

	// want[mode][state] is the counter bucket the lookup must fall into; only
	// bucketHit means the entry is reused instead of resolved.
	want := map[Mode]map[entryState]bucket{
		ModeNormal: {
			stateAbsent:        bucketMiss,
			stateSuccess:       bucketHit,
			stateError:         bucketHit,
			stateSuccessNoMeta: bucketMiss,
			stateUnknownStatus: bucketMiss,
		},
		ModeResolveAll: {
			stateAbsent:        bucketMiss,
			stateSuccess:       bucketRetried,
			stateError:         bucketRetried,
			stateSuccessNoMeta: bucketMiss,
			stateUnknownStatus: bucketMiss,
		},
		ModeRetryFailed: {
			stateAbsent:        bucketMiss,
			stateSuccess:       bucketHit,
			stateError:         bucketRetried,
			stateSuccessNoMeta: bucketMiss,
			stateUnknownStatus: bucketMiss,
		},
		ModeNoCache: {
			stateAbsent:        bucketMiss,
			stateSuccess:       bucketRetried,
			stateError:         bucketRetried,
			stateSuccessNoMeta: bucketMiss,
			stateUnknownStatus: bucketMiss,
		},
	}

	for _, mode := range modes {
		for _, state := range states {
			t.Run(mode.String()+"/"+string(state), func(t *testing.T) {
				expected := want[mode][state]

				p := NewPolicy(cacheFor(state), mode)
				entry, reuse := p.Lookup(testKey)

				if got, want := reuse, expected == bucketHit; got != want {
					t.Errorf("Lookup reuse = %v, want %v", got, want)
				}

				// The returned entry is only meaningful on a hit; on anything
				// else it must be zero so a caller can't accidentally write a
				// half-populated result into an output slot.
				if reuse {
					if entry.Status == "" {
						t.Error("Lookup returned a hit with a zero entry")
					}
				} else if entry != (Entry{}) {
					t.Errorf("Lookup returned entry %+v on a non-hit, want the zero Entry", entry)
				}

				assertCounters(t, p.Counters(), expected)

				// ShouldResolve is documented as the discard-the-entry view of
				// Lookup, so it must agree cell for cell.
				sp := NewPolicy(cacheFor(state), mode)
				if got, want := sp.ShouldResolve(testKey), expected != bucketHit; got != want {
					t.Errorf("ShouldResolve = %v, want %v", got, want)
				}
				assertCounters(t, sp.Counters(), expected)
			})
		}
	}
}

// assertCounters checks that exactly one counter moved, and that it was the
// expected one.
func assertCounters(t *testing.T, got Counters, expected bucket) {
	t.Helper()

	want := Counters{}
	switch expected {
	case bucketHit:
		want.Hits = 1
	case bucketMiss:
		want.Misses = 1
	case bucketRetried:
		want.Retried = 1
	}

	if got != want {
		t.Errorf("Counters = %+v, want %+v", got, want)
	}
	if got.Total() != 1 {
		t.Errorf("Total() = %d, want 1", got.Total())
	}
}

// The verbose "Cache: H hit, M miss, R retried" line is only trustworthy if the
// counters accumulate across a whole run and account for every ISBN considered.
func TestPolicyCountersAccumulate(t *testing.T) {
	c := New()
	c.Set("hit-1", Entry{Status: StatusSuccess, Metadata: &resolver.BookMetadata{Title: "One"}})
	c.Set("hit-2", Entry{Status: StatusSuccess, Metadata: &resolver.BookMetadata{Title: "Two"}})
	c.Set("retry-1", Entry{Status: StatusError, Error: "timeout"})

	p := NewPolicy(c, ModeRetryFailed)
	for _, key := range []string{"hit-1", "hit-2", "retry-1", "miss-1", "miss-2", "miss-3"} {
		p.Lookup(key)
	}

	want := Counters{Hits: 2, Misses: 3, Retried: 1}
	if got := p.Counters(); got != want {
		t.Errorf("Counters = %+v, want %+v", got, want)
	}
	if got := p.Counters().Total(); got != 6 {
		t.Errorf("Total() = %d, want 6 (one bucket per ISBN considered)", got)
	}
}

// ModeNoCache never loads a cache file, so it hands the policy a nil cache. That
// must behave as an empty cache rather than panicking mid-run.
func TestPolicyNilCache(t *testing.T) {
	for _, mode := range []Mode{ModeNormal, ModeResolveAll, ModeRetryFailed, ModeNoCache} {
		t.Run(mode.String(), func(t *testing.T) {
			p := NewPolicy(nil, mode)

			if _, reuse := p.Lookup(testKey); reuse {
				t.Error("Lookup on a nil cache reported a hit")
			}
			if got := p.Counters(); got != (Counters{Misses: 1}) {
				t.Errorf("Counters = %+v, want one miss", got)
			}
		})
	}
}

// Persists is what decides whether the run writes the cache file back. Only
// --no-cache opts out: --resolve-all and --retry-failed re-resolve precisely so
// they can refresh the entries they touched.
func TestModePersists(t *testing.T) {
	tests := map[Mode]bool{
		ModeNormal:      true,
		ModeResolveAll:  true,
		ModeRetryFailed: true,
		ModeNoCache:     false,
	}

	for mode, want := range tests {
		if got := mode.Persists(); got != want {
			t.Errorf("%s.Persists() = %v, want %v", mode, got, want)
		}
	}
}

func TestModeString(t *testing.T) {
	tests := map[Mode]string{
		ModeNormal:      "normal",
		ModeResolveAll:  "resolve-all",
		ModeRetryFailed: "retry-failed",
		ModeNoCache:     "no-cache",
		Mode(99):        "Mode(99)",
	}

	for mode, want := range tests {
		if got := mode.String(); got != want {
			t.Errorf("Mode(%d).String() = %q, want %q", int(mode), got, want)
		}
	}
}

// An unrecognised mode (a config or flag-wiring bug downstream) must fail
// closed by resolving, never by serving cached data the caller didn't ask for.
func TestPolicyUnknownModeResolves(t *testing.T) {
	p := NewPolicy(cacheFor(stateSuccess), Mode(99))

	if _, reuse := p.Lookup(testKey); reuse {
		t.Error("Lookup with an unrecognised mode reused a cached entry")
	}
	if got := p.Counters(); got != (Counters{Retried: 1}) {
		t.Errorf("Counters = %+v, want one retried", got)
	}
}

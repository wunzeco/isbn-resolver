# ISBN Resolver - Third Fallback API

## Feature Overview

~~~15% of a 488-ISBN sample fails to resolve against the current two-tier
fallback (Open Library, then Google Books).~~ **Superseded — see the
measurement below.** Add a third API tier to recover some of that failure
rate, choosing the specific API based on evidence from the actual failing
ISBNs rather than guessing.

**Measurement update (2026-08-05, final):** the §0 prerequisites have all
shipped and the official in-code re-measurement has been run. The true
dual-API-miss rate is **21/477 unique ISBNs ≈ 4.4%**, not ~15% — the
originally-reported gap was overwhelmingly Google Books quota exhaustion, not
missing catalog data. Every count below predating "Re-measurement (2026-08-05,
official)" in §1 is a superseded historical figure, retained to show how the
number moved.

**Decision (2026-08-05, final): no third tier.** The project owner reviewed
the 4.4% residual and decided it does not justify either candidate's cost —
a MARCXML parser for Library of Congress, or a paid subscription for ISBNdb.
**§2–§4 below are kept for the record (the investigation was real and the
API research may be useful if the gap grows) but are explicitly closed,
not merely deferred.** No further work is planned against this spec unless
the failure rate becomes a problem again at a materially larger scale.

**Investigation update (2026-08-05):** the §1 investigation has been run
against the real sample (`examples/ISBNs.csv`, ~~489~~ 490 ISBNs) and surfaced
two likely bugs inflating the measured failure rate — see §0. Fix those first;
they're lower-cost and lower-risk than building a third API, and may close
most of the gap on their own. Re-measure before committing to §2. *(Both are
now fixed and the re-measurement is recorded in §1; this closed as predicted —
the bugs were the bulk of the gap.)*

## Current State

`resolver.APIClient.Resolve` (`pkg/resolver/client.go:42-56`) tries Open
Library, then falls back to Google Books, then gives up:

```go
func (c *APIClient) Resolve(isbn string) (*BookMetadata, error) {
    metadata, err := c.fetchFromOpenLibrary(isbn)
    if err == nil && metadata != nil {
        return metadata, nil
    }
    metadata, err = c.fetchFromGoogleBooks(isbn)
    if err == nil && metadata != nil {
        return metadata, nil
    }
    return nil, fmt.Errorf("failed to resolve ISBN from all APIs")
}
```

Both fetch methods already support retry/backoff and a shared rate limiter
(`pkg/resolver/retry.go`, `pkg/resolver/limiter.go`), and base URLs are
injectable for testing (`client_test.go`). A third tier slots into this
same shape.

## Requirements

### 0. Prerequisite: fix two likely-inflating bugs before building anything (do this first)

Running the resolver against `examples/ISBNs.csv` (489 ISBNs, 74 unique
failures, 15.6% — matching the reported rate) and probing both APIs
directly for each failing ISBN found:

- **Open Library genuinely has no data** for all 74 failing ISBNs
  (confirmed via direct `api/books?bibkeys=...` calls) — not a bug.
- **Google Books returned HTTP 429 for all 74**, both during the original
  run and again ~7 minutes later on a fully sequential
  (`--concurrency 1`, no cache) retry pass. The block persisted across
  minutes, consistent with a sustained quota exhaustion rather than a
  brief per-second burst limit.

Two concrete bugs are very likely contributing to that quota exhaustion:

1. **`APIClient.Limiter` is built and unit-tested but never wired up.**
   `pkg/resolver/limiter.go`'s `RateLimiter` exists specifically to
   proactively pace requests and avoid triggering 429s (per
   `specs/performance-caching.md` §4), but `grep -rn "\.Limiter" cmd/
   pkg/resolver/pool.go` shows it is never assigned — `main.go` constructs
   exactly one shared `APIClient` (`cmd/isbn-resolver/main.go:109`) and
   passes it into the worker pool, but never sets `client.Limiter` on it.
   Every pool worker hits Google Books with zero proactive pacing, and
   each failing ISBN can fire up to 4 requests (1 + `MaxRetries` retries)
   with no coordination across workers — a fast way to exhaust a shared
   IP-level quota.
2. **No Google Books API key is sent.** `fetchFromGoogleBooks`
   (`pkg/resolver/client.go`) builds its request URL with no `key=`
   parameter, so every request shares Google's much lower anonymous/
   unauthenticated quota tier instead of a registered project's higher
   quota.

**Fix, in order:**
- Wire the limiter: `client.Limiter = resolver.NewRateLimiter(rate, burst)`
  on the single shared client before it's passed into `resolveISBNs`
  (`cmd/isbn-resolver/main.go:109-119`). Rate/burst should be configurable
  (reuse `cfg.RateLimit` or add a sibling field) rather than hardcoded.
- Add optional Google Books API key support: a `--google-books-api-key`
  flag / `ISBN_GOOGLE_BOOKS_API_KEY` env var (matching the existing
  `ISBN_*` convention), appended as `&key=<key>` to the Google Books
  request URL when set. Must degrade gracefully (current anonymous
  behavior) when unset — this cannot become a hard requirement to run the
  tool.
- Files: `cmd/isbn-resolver/main.go`, `pkg/resolver/client.go`,
  `pkg/config/config.go`.
- Done when: a `httptest`-backed test proves `client.Limiter` is
  non-nil and consulted by the pool path, a test proves `key=` is appended
  when configured and omitted when not, and a re-run of
  `examples/ISBNs.csv` (once Google Books' quota for this environment
  resets, or using a valid API key) produces a measurably different
  failure count than the current 76/488 baseline.

This step **must** complete, with a fresh measurement recorded, before §1's
investigation numbers below are treated as reflecting genuine data gaps
rather than quota exhaustion — the current failing-ISBN list is contaminated
by the bug and cannot yet be used to decide between the two candidate APIs
in §2 with confidence.

**§0 is complete (2026-08-05).** Both bugs are fixed in code (shared limiter
wired onto the client; `--google-books-api-key` / `ISBN_GOOGLE_BOOKS_API_KEY`
plumbed through to the request URL), and the fresh measurement this step
gates on is recorded under "Re-measurement (2026-08-05, official)" in §1.
§1's numbers may now be read as genuine data gaps.

**Confirmed 2026-08-05, with the limiter fix live and a Google Books API key
tested directly (out-of-band, ahead of the code implementing key support).
The official in-code run has since reproduced this out-of-band probe exactly —
same 21 residual ISBNs, same 53 recovered by Google Books:**

- The limiter fix alone (shipped) did **not** change the failure set at all —
  re-running the full ~~489~~-ISBN (490) sample produced the identical 74 unique
  failures. Verbose output showed 222 "rate limited by Google Books" retry
  warnings; a direct probe confirmed Google Books was still returning 429 for
  every one of the 74. The quota exhaustion is not a short burst window that
  pacing fixes — it is a longer-lived (apparently daily) per-IP quota that
  had already been spent by repeated testing.
- Querying Google Books directly **with an API key** for all 74 previously-
  failing ISBNs (bypassing the anonymous quota entirely) found: **50 resolve
  immediately**, 8 hit a transient 503 and were retried, of which **3 more
  resolve** and 5 confirm no data. Final tally: **53 of 74 (71.6%) actually
  have Google Books data** — the API key, not the limiter, was the fix that
  mattered. **21 of 74 (28.4%) are genuinely absent from both APIs.**
- **True dual-API-miss rate: ~~21/488 ≈ 4.3%~~ → 21/475 unique ≈ 4.4%** (the
  denominator is corrected below; the numerator held), not the original 76/488
  (15.6%). Adding the API key alone — no third API tier — recovers the large
  majority of the originally-reported gap.
- The remaining 21 genuine misses show a rough pattern: five sequential
  `978-0-1801042-0xx` ISBNs (one publisher's block), two `978-5` (Russian-
  language imprint) titles, and several `978-0-241` (Penguin) / `978-0-746`
  (Usborne) ISBNs — an odd miss for major publishers, possibly audiobook-only
  editions or very recent releases not yet indexed by either catalog.
- **Implication for §2:** with the API key implemented (~~still queued in
  `IMPLEMENTATION_PLAN.md`~~ — now shipped), the LC-vs-ISBNdb decision should
  be re-evaluated against a ~~~4.3%~~ 4.4% true gap, not 15.6% — a materially
  weaker case for taking on either candidate's cost (MARC parsing for LC, or a
  paid subscription for ISBNdb). Recommend treating §2–§4 as on hold pending an
  explicit decision once the key is live in code and the plan's own
  "Re-measure the failure rate" item runs its official, in-code measurement.
  *(That measurement has now run — see §1. §2–§4 remain on hold.)*

### 1. Investigation (must happen before picking the third API)

Do this first — it determines which of §2's two candidate APIs is worth
building, and whether a third tier is even the right fix (as opposed to,
e.g., a bug in ISBN normalization causing false failures).

**Correction (2026-08-05): the sample is 490 ISBNs, not 488 or 489.**
`examples/ISBNs.csv` has CRLF endings and no trailing newline, so `wc -l`
under-reports it by one. It is 491 content lines = 1 header row + 490 ISBNs,
of which 477 are unique (13 duplicate rows, not the 2 recorded below). The
header row was also being fed in as an ISBN until `scanISBNs` learned to skip
it, so every failure count quoted in this spec has an inflated numerator *and*
an unreliable denominator. Treat the numbers below as indicative only; the
plan's re-measurement item supersedes them.

**Status: historical (pre-§0-fix) run, superseded — kept below to show how
the number moved.** What was known from the first `examples/ISBNs.csv` run,
before the limiter/API-key bugs were fixed:

- 76/488 rows failed (74 unique ISBNs; 2 duplicate rows in the source
  file), a 15.6% failure rate, matching the originally reported ~15%.
- All 74 fail Open Library with genuine "no data" responses — this part of
  the number is real and will persist regardless of §0's fixes.
- All 74 also currently fail Google Books with HTTP 429, which per §0 is
  likely quota exhaustion, not "no data" — meaning the true dual-API-miss
  rate is unknown until §0 is fixed and this is re-measured.
- Rough ISBN-prefix patterns worth revisiting once real no-data ISBNs are
  isolated: a `979-8` (US self-publishing registrant range) ISBN, two
  `978-5` (Russian-language imprint) ISBNs, and thirteen sequential
  `978-0-85231-6xxx` ISBNs from what looks like one small publisher's full
  backlist — these look like plausible genuine catalog gaps rather than
  quota artifacts, but can't be confirmed until Google Books is queryable
  again for this environment.

**Re-measurement (2026-08-05, official).** With §0's fixes shipped
(limiter wired, `--google-books-api-key` plumbed through) and
`ISBN_GOOGLE_BOOKS_API_KEY` configured, the resolver was re-run against the
corrected `examples/ISBNs.csv` sample (490 rows, 477 unique ISBNs, header
row excluded by the `scanISBNs` fix):

- **21/477 unique ISBNs genuinely unresolvable by both APIs ≈ 4.4%** — down
  from the original 74/477 (15.5%) pre-fix figure.
- This matches, ISBN-for-ISBN, the out-of-band probe run directly against
  Google Books with the API key ahead of the code landing (see the
  "Confirmed 2026-08-05" bullets above §1): same 21 residual ISBNs, same 53
  recovered once the key unblocked Google Books.
- The `979-8`/`978-5`/sequential-backlist prefix pattern noted above holds
  up in the final 21 — they are genuine catalog gaps, not quota artifacts.
- **Go/no-go for §2:** left to the plan's separate "Categorise the
  genuinely-unresolvable ISBNs and decide LC vs ISBNdb" item — this item's
  scope is the measurement itself, not the resulting API decision.

- Run the resolver's existing `--verbose` output (or a small one-off script
  reusing `pkg/resolver`) against the full 488-ISBN sample and capture the
  list of ISBNs that fail both Open Library and Google Books, with each
  API's specific error/response (not-found vs. malformed response vs.
  network error).
- For each failing ISBN, check by hand (a handful is enough, doesn't need
  automation) whether it's: pre-1970, non-US-published, self-published/
  small-press, or a data-entry issue (invalid ISBN checksum that's slipping
  past `isbn.Validate`, or a valid ISBN for an edition neither API happens
  to have indexed).
- Record the breakdown. This decides §2: if failures cluster around
  older/US-catalogued works, Library of Congress is the better bet: if
  they're broadly obscure/small-press/international with no clear pattern,
  ISBNdb's larger index is more likely to help.
- Done when: a short table exists (in `PROGRESS.md` or a throwaway note) of
  failure counts by category, and a go/no-go decision on which API from §2
  to build is made explicitly, not assumed.

### 2. Candidate APIs (pick one based on §1, do not build both speculatively)

**Closed, no-go (2026-08-05) — kept for reference only, not actionable.**
See the Decision note under Feature Overview.

**Option A — Library of Congress SRU/MARC API**
- Free, no API key, no rate-limit-by-subscription-tier concerns.
- Endpoint shape: `http://lx2.loc.gov:210/lcdb?version=1.1&operation=searchRetrieve&query=bath.isbn=<isbn>&recordSchema=marcxml`.
- Returns MARCXML, not JSON — needs a MARC field-tag parser (title is
  245, author is 100/700, publisher/date is 260 or 264, page count is 300).
  This is the main implementation cost: nothing in the codebase parses
  MARC today, so it's new surface area, not just another JSON `Get`.
- Best expected coverage: older and U.S.-catalogued works. Weak on
  self-published, very recent, or non-U.S. small-press titles.

**Option B — ISBNdb**
- Paid (subscription API key required — cost and account setup are real
  adoption costs, not just code).
- Returns JSON in a shape close to the existing two APIs, so
  implementation cost is comparable to `fetchFromGoogleBooks`, not to
  Option A's MARC parsing.
- Best expected coverage: broad, including small-press/self-published/
  long-tail titles that library-catalog-based sources (Open Library, LC)
  are more likely to miss.

### 3. Implementation (once an API is chosen)

- Add `fetchFrom<API>(isbn string) (*BookMetadata, error)` to
  `pkg/resolver/client.go`, following the existing two methods' shape:
  own base URL field (injectable, matching `OpenLibraryBaseURL`/
  `GoogleBooksBaseURL`'s pattern), routed through `doWithRetry` and the
  shared `Limiter` like the existing two.
- If the chosen API requires an API key (ISBNdb): add `APIKey` to
  `APIClient`, sourced from config (`pkg/config`) and an env var
  (`ISBN_ISBNDB_API_KEY`, matching the `ISBN_*` env var convention already
  used for `ISBN_CACHE_FILE`/`ISBN_CONCURRENCY`), never hardcoded or
  logged. Missing key should make `Resolve` skip this tier gracefully
  (falling through to "no data found"), not error the whole run — the tool
  must keep working for users who don't have a key.
- Extend `Resolve` to try the new tier after Google Books, same
  `err == nil && metadata != nil` pattern.
- Files: `pkg/resolver/client.go`, `pkg/resolver/<api>_test.go`.
- Done when: a `httptest`-backed test proves the third tier is only
  reached when the first two return no data, and resolves ISBNs from §1's
  failing list that the third API is known (from manual spot-checking) to
  actually carry.

### 4. Verbose Output

- **Decided (2026-08-05): implemented, ahead of the third tier.** `Resolve`
  now returns the answering tier's name alongside the metadata, and the
  verbose line reads
  `✓ Resolved ISBN 9780134190440: The Go Programming Language (via Open Library)`.

  It was brought forward rather than shipped with the third tier because
  §1's re-measurement is what the remaining items are blocked on, and that
  measurement is far more useful when it can say *which* tier carried each
  success — "how much is the fallback actually doing?" is otherwise
  unanswerable from a run's output. The suffix is appended, so the line's
  existing prefix is unchanged.

  The tier name describes one resolution, not the book, so it rides on
  `resolver.Result` rather than on `BookMetadata` — this deliberately keeps
  it out of the JSON/CSV output schema and out of the cache file. A
  consequence worth knowing: a cache hit prints no resolved line at all, so
  the source is only ever reported for a fresh resolution.

  `specs/performance-caching.md` §"Expected Output (Verbose Mode)" has been
  updated to the same line shape, so the two specs do not disagree.

## Non-Goals

- Trying to build both candidate APIs "just in case" — §1's investigation
  should make the choice, not hedging.
- A fourth+ fallback tier. If two API tiers plus this one still leaves a
  meaningful failure rate, that's a signal to revisit rather than keep
  stacking APIs.
- Retrying failed ISBNs against the new tier automatically for users who
  already have those ISBNs cached as `error` in the local cache — that's
  what `--retry-failed` already exists for (`specs/performance-caching.md`).

## Testing Requirements

1. **Unit Tests**
   - New tier is only called when the first two return no data (mock all
     three, assert call counts).
   - New tier's response parsing (JSON for ISBNdb, or MARC field
     extraction for LC) against a fixture response.
   - Missing API key (if applicable) skips the tier without erroring the
     whole `Resolve` call.

2. **Integration Tests**
   - Re-run the full 488-ISBN sample (or the subset that failed in §1)
     through the three-tier resolver and confirm a measurably lower
     failure rate; record the before/after numbers.

3. **Manual Testing Checklist**
   - [ ] §1's investigation completed and API choice documented before any
         code is written
   - [ ] A previously-failing ISBN from the sample now resolves via the
         new tier
   - [ ] A well-known ISBN still resolves via Open Library (tier 1) without
         hitting the new tier — no regression in the common case
   - [ ] Missing/invalid API key (if applicable) degrades gracefully rather
         than crashing the run

## Success Criteria

- [ ] §1 investigation produces a documented failure breakdown and API
      choice
- [ ] Third tier implemented, tested, and wired into `Resolve`
- [ ] Measured failure rate on the 488-ISBN sample drops from ~15%
- [ ] No regression in resolution speed/behavior for ISBNs the first two
      tiers already handle

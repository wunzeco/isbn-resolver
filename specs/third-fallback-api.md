# ISBN Resolver - Third Fallback API

## Feature Overview

~~~15% of a 488-ISBN sample fails to resolve against the current two-tier
fallback (Open Library, then Google Books).~~ **Superseded — both figures
were wrong: the sample is 490 rows / 477 unique ISBNs, and the true rate is
4.4%. See the measurement below.** Add a third API tier to recover some of that failure
rate, choosing the specific API based on evidence from the actual failing
ISBNs rather than guessing.

**Measurement update (2026-08-05, final):** the §0 prerequisites have all
shipped and the official in-code re-measurement has been run. The true
dual-API-miss rate is **21/475 unique valid ISBNs ≈ 4.4%**, not ~15% — the
originally-reported gap was overwhelmingly Google Books quota exhaustion, not
missing catalog data. Every count below predating "Re-measurement (2026-08-05,
official)" in §1 is a superseded historical figure, retained to show how the
number moved.

**Re-run (2026-08-06):** the 2026-08-05 run's residual list was never
recorded, so it was re-derived from a fresh `--no-cache` run, which found
**22/475 ≈ 4.6%** and lists every ISBN individually — see §1 "Residual
unresolvable ISBNs". The one-ISBN difference is live-catalog drift between two
runs a day apart and changes no decision; the 22 are the auditable set.

**Sample size audit (2026-08-06, canonical).** Every ISBN count in this
document has been re-checked against `examples/ISBNs.csv` itself. These are
the canonical figures and the only ones that should be quoted going forward:

| Figure | Value | How to reproduce |
|---|---|---|
| Content lines | 491 | `tr -d '\r' < examples/ISBNs.csv \| grep -c ''` |
| Header rows | 1 (`ISBN`) | first line; skipped by `scanISBNs` |
| ISBN rows | **490** | pinned by `TestGetISBNsOnTheMeasurementSample` (`cmd/isbn-resolver/main_test.go`) |
| Unique ISBNs | **477** | 13 duplicate rows across 12 ISBNs, one of which appears three times |
| Invalid ISBNs (bad ISBN-13 checksum) | **2** | `9781782955129` (line 210), `9780141371284` (line 343); rejected by `isbn.Validate` before any request |
| ISBN rows reaching the resolver | **488** | 490 rows − the 2 invalid; `Processing 488 valid ISBN(s)` in `--verbose` |
| Unique *valid* ISBNs | **475** | 477 unique − the 2 invalid; this is the denominator a resolution rate is actually measured over |
| Genuinely unresolvable, post-§0 | **22** (4.6% of 475) | §1 "Residual unresolvable ISBNs (2026-08-06)" — listed individually there |

`wc -l` reports 490 for this file — CRLF endings with no trailing newline mean
the final line goes uncounted — which is where the 489 figure came from, and
the header row was counted as an ISBN on top of that, which is where 488 and
the inflated failure numerators came from. **Every appearance of 488, 489,
76/488, 15.6% or ~15% below is superseded.** (15.5% is *not* superseded:
it is the corrected pre-fix rate, 74/477.) **475 is not superseded either** —
the 2026-08-06 audit called it a slip for 477, and that correction was itself
wrong: 475 is the count of unique *valid* ISBNs, the only denominator the
resolver ever sees, and it is the more honest one to quote a miss rate against
since the 2 invalid ISBNs are a data-entry problem rather than a catalog gap.
Figures are annotated in place
rather than rewritten, because only the pre-fix run can say what the pre-fix
run measured — the record of how the number moved is worth more than a
document that reads as though it were always right.

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

Running the resolver against `examples/ISBNs.csv` (~~489 ISBNs~~ 490 rows /
477 unique, 74 unique failures, ~~15.6%~~ 15.5% of unique — matching the
reported rate) and probing both APIs directly for each failing ISBN found:

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
  failure count than the current ~~76/488~~ 74/477-unique baseline (the 76
  counted failing *rows*, one of which was the header line; superseded per
  the sample size audit).

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
  re-running the full ~~489~~ 490-row (477 unique) sample produced the identical 74 unique
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
- **True dual-API-miss rate: ~~21/488 ≈ 4.3%~~ → 21/475 unique valid ≈ 4.4%**
  (the numerator held throughout; only the denominator moved. The 2026-08-06
  audit annotated the 475 here as a slip for 477 — **that annotation was
  wrong and has been withdrawn**: 475 is 477 unique minus the 2 ISBNs with a
  bad checksum, i.e. the ISBNs the resolver actually attempted, which is the
  right denominator for a miss rate), not the original
  ~~76/488 (15.6%)~~ 74/477 (15.5%). Adding the API key alone — no third API
  tier — recovers the large majority of the originally-reported gap.
- The remaining 21 genuine misses show a rough pattern: five sequential
  `978-0-1801042-0xx` ISBNs (one publisher's block), two `978-5` (Russian-
  language imprint) titles, and several `978-0-241` (Penguin) / `978-0-746`
  (Usborne) ISBNs — an odd miss for major publishers, possibly audiobook-only
  editions or very recent releases not yet indexed by either catalog.
  *(Consistency-checked against the sample 2026-08-06: it contains exactly
  five unique ISBNs in that block and exactly two `978-5`, so both counts mean
  "all of them in the sample failed". Note the block is `978-1-801042-0xx`
  — `9781801042055`–`9781801042109` — not the `978-0-…` written above.
  "Several" is not checkable: the 21 were never listed individually.)*
- **Implication for §2:** with the API key implemented (~~still queued in
  `IMPLEMENTATION_PLAN.md`~~ — now shipped), the LC-vs-ISBNdb decision should
  be re-evaluated against a ~~~4.3%~~ 4.4% true gap, not ~~15.6%~~ 15.5% — a materially
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
of which 477 are unique (13 duplicate rows across 12 ISBNs — one appears three
times — not the 2 recorded below). The header row was also being fed in as an
ISBN until `scanISBNs` learned to skip it, so every failure count quoted in
this spec has an inflated numerator *and* an unreliable denominator. Treat the
numbers below as indicative only; the "Re-measurement (2026-08-05, official)"
subsection supersedes them, and the sample size audit under Feature Overview
is the canonical statement of the sample's own figures.

**Status: historical (pre-§0-fix) run, superseded — kept below to show how
the number moved.** What was known from the first `examples/ISBNs.csv` run,
before the limiter/API-key bugs were fixed:

- ~~76/488 rows failed (74 unique ISBNs; 2 duplicate rows in the source
  file), a 15.6% failure rate~~, matching the originally reported ~15%.
  **Superseded per the sample size audit:** the sample is 490 rows / 477
  unique with 13 duplicate rows, and one of those 76 failing "rows" was the
  `ISBN` header line. The unique-ISBN figure that survives the correction is
  **74/477 = 15.5%**.
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
  *(Counts checked against the sample 2026-08-06: it holds exactly one `979-8`
  ISBN, exactly two `978-5`, and 23 unique `978-0-85231-6xxx` — so the 13 that
  failed here were a little over half of that block, not "the full backlist".
  Whether any of the three clusters survived into the post-fix 21 is a
  separate question — see the re-measurement below.)*

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
- ~~The `979-8`/`978-5`/sequential-backlist prefix pattern noted above holds
  up in the final 21~~ — **corrected 2026-08-06:** that sentence overstated
  what was measured. The description of the final 21 recorded above §1 names
  a *different* sequential block (five `978-0-1801042-0xx`), the same two
  `978-5`, and "several" `978-0-241`/`978-0-746` — it does not name the
  `979-8` ISBN or the thirteen `978-0-85231-6xxx`, which the API key
  therefore appears to have recovered. Only the `978-5` pair is common to
  both descriptions. The per-ISBN list of the 21 was never written down, so
  no stronger claim than that is checkable; the clusters are genuine catalog
  gaps, but *which* clusters is only as precise as the prose above.
  *(Settled 2026-08-06 by a fresh re-run — see "Residual unresolvable ISBNs"
  below. The recovery of the `979-8` ISBN and of 18 of the 19
  `978-0-85231-6xxx` ISBNs is confirmed; the `978-5` pair is confirmed to
  survive.)*
- **Go/no-go for §2:** left to the plan's separate "Categorise the
  genuinely-unresolvable ISBNs and decide LC vs ISBNdb" item — this item's
  scope is the measurement itself, not the resulting API decision.

#### Residual unresolvable ISBNs (2026-08-06) — the list, at last

The 2026-08-05 re-measurement recorded only the *count* (21) and a prose
sketch of the prefix clusters. The per-ISBN list was never written down, which
is why several claims above ("several `978-0-241`/`978-0-746`", whether the
`979-8` and `978-0-85231-6xxx` clusters survived) were left uncheckable, and
why the §2 no-go rested on evidence nobody could re-inspect.

**The original list is unrecoverable — `~/.isbn-resolver/cache.json` holds the
*pre-fix* 74, not the post-fix 21** (the official run used `--no-cache`, which
neither reads nor writes the cache, so the file still contains the earlier
unpaced/keyless run: 476 entries, 74 `error`, all stamped 2026-08-05T06:52Z).
So the list below comes from a **fresh re-run**, not from the 2026-08-05 run:

```
go build -o /tmp/isbn-resolver ./cmd/isbn-resolver
ISBN_GOOGLE_BOOKS_API_KEY=<key> /tmp/isbn-resolver \
  --no-cache --verbose --format json --file examples/ISBNs.csv
```

488 valid rows processed in 2m36s; **22 unique ISBNs unresolvable**, every one
of them reporting `Open Library: no data found for ISBN; Google Books: no data
found for ISBN` — no 429s, no transient failures, no network errors in the
residual set. (Six `rate limited by Google Books` warnings fired during the
run, all on ISBNs that then resolved on retry, so none contaminate this list.)

| Cluster | Count | ISBNs |
|---|---|---|
| `978-0-241` (Penguin) | 4 | 9780241316016, 9780241426968, 9780241620328, 9780241692295 |
| `978-1-801042-0xx` (sequential block) | 5 | 9781801042055, 9781801042062, 9781801042079, 9781801042093, 9781801042109 |
| `978-0-746` (Usborne) | 3 | 9780746091203, 9780746091227, 9780746091326 |
| `978-5` (Russian-language imprint) | 2 | 9785960464659, 9785960466813 |
| Singletons | 8 | 9780709724223, 9780852316283, 9781406394986, 9781408347843, 9781838167639, 9781839134494, 9781848531840, 9781917067294 |

**22, not 21.** The two runs are a day apart against live third-party
catalogs, and the original 21 was never listed, so the one-ISBN delta cannot
be attributed to a specific ISBN — it is equally consistent with a title
being de-indexed, with one of the 8 transient-503 probes in the out-of-band
run having been scored optimistically, or with ordinary catalog churn. It
does not change any decision: 22/475 is 4.6%, still the same order as 4.4%.

This list also settles the cluster questions the prose could not:

- The `979-8` ISBN (`9798885870337`) and **18 of 19** `978-0-85231-6xxx`
  ISBNs were recovered by the API key — only `9780852316283` remains. The
  pre-fix cache holds 19 failures in that block, not the "thirteen" recorded
  above; 19 is the figure to trust, since it is read from the run's own
  cache file rather than from a description of it.
- The `978-5` pair survived intact, as the prose claimed.
- "Several `978-0-241`/`978-0-746`" is now 4 and 3.
- The sequential block is confirmed as `978-1-801042-0xx`
  (9781801042055–9781801042109), not the `978-0-1801042-0xx` written above,
  and all five members of it in the sample fail.

Nothing here reopens the §2 no-go: the residual is still small, still spread
across mainstream publishers rather than concentrated in the older/US-
catalogued works Library of Congress would cover, and still not a case either
candidate API would obviously fix.

- Run the resolver's existing `--verbose` output (or a small one-off script
  reusing `pkg/resolver`) against the full ~~488~~ 490-row sample and capture the
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
   - Re-run the full ~~488~~ 490-row (477 unique) sample (or the subset that failed in §1)
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
- [ ] Measured failure rate on the ~~488-ISBN~~ 490-row / 477-unique sample
      drops from ~~~15%~~ 15.5% (74/477) — met by §0's fixes alone, at
      21/477 = 4.4%, without a third tier
- [ ] No regression in resolution speed/behavior for ISBNs the first two
      tiers already handle

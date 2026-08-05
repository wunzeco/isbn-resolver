# ISBN Resolver - Deferred Caching Features

## Feature Overview

`specs/performance-caching.md` shipped the core caching/concurrency work
(local cache, `--resolve-all`/`--retry-failed`/`--no-cache`, bounded worker
pool, rate limiting, backoff). Three items were deliberately deferred out of
that spec rather than built. This spec covers whether and how to build them
now that the core work is done, and is honest that not all three are equally
actionable.

## Current State

`IMPLEMENTATION_PLAN.md`'s Deferred section lists three items, carried over
verbatim from `specs/performance-caching.md`:

1. Sheet-based cache alternative reading the `Status` column
   (`specs/performance-caching.md` §5 — design note only, not required for
   v1).
2. Distributed/shared caching across multiple users or machines (spec
   Non-Goals).
3. Cache TTL/expiry (spec Non-Goals) and multi-ISBN request batching (spec
   Non-Goals), grouped together in the plan's one bullet.

The local file cache (`pkg/cache`), worker pool (`pkg/resolver/pool.go`),
and rate limiter (`pkg/resolver/limiter.go`) are all implemented, tested,
and documented — this spec only concerns the three deferred items above.

## Requirements

### 1. Sheet-Based Cache Alternative

Status: worth building. This was deferred only because it wasn't required
for the initial implementation, not because it's a bad idea — it directly
serves the tool's primary use case (running against a growing Google Sheet
from CI or another ephemeral environment where a local `~/.isbn-resolver/cache.json`
doesn't persist between runs).

- Before resolving, if the configured output range already has data (i.e.
  this isn't the first write), read the existing `Status` column
  (`sheets.ReadISBNs`-style read, but over the output range rather than the
  input range) and treat any row marked `Success` the same way the local
  cache's `Normal` mode treats a cached success: skip re-resolving that
  ISBN, reuse its existing row's metadata unless overwritten.
- This is additive to the local file cache, not a replacement — both can be
  enabled at once, and either can be disabled independently
  (`--no-cache` continues to mean "ignore both").
- New flag: `--sheet-cache` (default off) to opt in, since it costs an
  extra read call and assumes a specific output column layout
  (`sheets.WriteConfig`'s existing `ISBN-13, Title, ..., Status, Error`
  schema from `pkg/sheets/writer.go:formatResultsForSheet`).
- `--resolve-all`/`--retry-failed` apply to the sheet cache the same way
  they apply to the local cache: `--resolve-all` ignores prior `Status`
  entirely, `--retry-failed` still skips rows marked `Success` but
  re-attempts rows marked `Error`.
- Files: `pkg/sheets/reader.go` (new `ReadExistingStatus` or similar),
  `cmd/isbn-resolver/main.go` (wire into the same `cache.Policy` decision
  point already used for the local cache, or a second policy pass — decide
  during implementation which reads cleaner given `resolveISBNs`'s current
  shape).
- Done when: a second run against the same sheet range with `--sheet-cache`
  and no local cache file makes zero network calls for rows already marked
  `Success`, verified against a real or `httptest`-backed Sheets API.

### 2. Distributed/Shared Caching

Status: not recommended without a concrete trigger. The tool is a
single-user CLI invoked against a personal or team Google Sheet; there's no
current evidence of multiple people/machines racing to resolve the same
ISBN list concurrently. Building this now would mean guessing at
requirements (a shared store — Redis? S3? the sheet itself, which is
requirement #1 above and arguably already solves the "shared across
invocations" problem for the sheet case).

- Do not implement speculatively. If this becomes a real need, revisit as
  its own spec once the concrete scenario is known (e.g. "CI runs this from
  N parallel jobs against the same sheet" — the sheet-based cache above
  already covers cross-invocation coordination without a new subsystem, so
  confirm requirement #1 is insufficient before scoping this).

### 3. Cache TTL/Expiry

Status: not recommended without a concrete trigger, same reasoning as the
original spec's non-goal. Book metadata (title, authors, publisher, page
count) essentially never changes after publication. The one field that
could plausibly drift — categories/subjects, which Open Library and Google
Books occasionally re-tag — isn't worth an expiry mechanism when
`--resolve-all` already exists as an explicit, user-controlled way to
refresh everything.

- Do not implement. If a real staleness complaint arises, prefer teaching
  users to run `--resolve-all` periodically over adding a TTL that silently
  re-resolves ISBNs the user didn't ask to refresh (surprising cost/latency
  on an otherwise-fast cached run).

### 4. Multi-ISBN Request Batching

Status: not actionable — this isn't a deferred feature so much as a hard
blocker. Confirmed against both APIs the resolver calls
(`pkg/resolver/client.go`):

- Open Library's `bibkeys` parameter (`api/books?bibkeys=ISBN:x,ISBN:y,...`)
  does technically accept multiple comma-separated keys in one request, but
  the resolver doesn't currently use this — only `pkg/resolver/pool.go`'s
  concurrent *individual* requests. This is worth a narrow follow-up:
  Open Library batching would reduce request count (helping the rate
  limiter's job) without changing the pool's per-ISBN interface much,
  since one response would need to be fanned out to N result slots instead
  of N requests each returning one result.
- Google Books' `volumes?q=isbn:X` endpoint has no multi-ISBN batch form —
  each ISBN requires its own request regardless.
- Given Google Books has no batch path at all, full request batching across
  both APIs isn't achievable; only Open Library's half could be batched,
  and doing so would fragment the two fetch paths' shapes (one batched,
  one not) for a benefit the rate limiter and worker pool already deliver
  most of.
- Recommendation: do not implement. If Open Library rate limits become a
  practical problem even with the token bucket in place, revisit
  batching Open Library requests specifically as its own narrow spec —
  don't block on this now.

## Non-Goals (unchanged from the parent spec)

- Distributed/shared caching (§2 above — deferred pending a concrete
  trigger).
- Cache TTL/expiry (§3 above — deferred pending a concrete trigger).
- Full cross-API request batching (§4 above — not achievable given Google
  Books' API shape; Open Library-only batching is a possible narrow
  follow-up, not scoped here).

## Testing Requirements (for §1, the only item recommended for implementation)

1. **Unit Tests**
   - Sheet-cache status parsing: `Success` rows are skipped, `Error` rows
     are retried under `--retry-failed`, all rows re-resolved under
     `--resolve-all`.
   - `--sheet-cache` combined with a local cache file: both consulted,
     either being a hit is sufficient to skip.
   - `--no-cache` disables both the sheet cache and the local cache.

2. **Integration Tests**
   - A second run against a fixture sheet (via `httptest`-backed Sheets API,
     matching the pattern in `cmd/isbn-resolver/integration_test.go`) with
     `--sheet-cache` and an empty/absent local cache file makes zero
     resolver network calls for previously-successful rows.

3. **Manual Testing Checklist**
   - [ ] `--sheet-cache` on a sheet with existing `Success`/`Error` rows
         skips successes and re-attempts errors under `--retry-failed`
   - [ ] `--resolve-all --sheet-cache` re-resolves everything despite
         existing `Success` rows
   - [ ] Running from a fresh CI checkout (no local cache file) still skips
         already-resolved rows when `--sheet-cache` is set

## Success Criteria

- [ ] `--sheet-cache` implemented, tested, and documented (README + example
      configs, matching how the local cache flags were documented)
- [ ] Distributed caching, TTL/expiry, and full request batching remain
      explicitly un-implemented, each with the reasoning above preserved
      in this spec so a future revisit doesn't have to re-derive it
- [ ] Open Library-only batching identified as a possible narrow follow-up
      but explicitly out of scope for this spec

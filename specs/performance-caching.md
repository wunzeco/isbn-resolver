# ISBN Resolver - Performance & Caching Feature

## Feature Overview

Speed up repeated runs against growing Google Sheets by skipping ISBNs that
have already been resolved successfully, and speed up large first-time runs
by resolving multiple ISBNs concurrently under a rate limiter. Today the tool
re-resolves every ISBN on every run and does so strictly one at a time, so
runtime grows unbounded with sheet size.

## Current State

- `main.go` loops over every valid ISBN sequentially and calls
  `resolver.Resolve` one at a time (`cmd/isbn-resolver/main.go:101-116`).
  There is no concurrency.
- `resolver.APIClient.Resolve` makes up to two sequential HTTP calls per ISBN
  (Open Library, then Google Books as fallback) with no retry/backoff on
  failure or rate limiting (`pkg/resolver/client.go:42-56`).
- There is no cache of any kind. Every invocation re-resolves the full input
  list regardless of prior runs.
- `sheets.WriteResults` always overwrites the configured output range with
  the full result set (`pkg/sheets/writer.go:20-65`); it does not read
  existing sheet content first, so there's no way today to tell which rows
  were already resolved.

## Problem

As the number of ISBNs in a tracked sheet grows, run time grows linearly
because:
1. Previously-resolved ISBNs are resolved again on every run (wasted work).
2. All resolution happens sequentially (no parallelism).
3. Failures (including rate-limit responses) are not retried with backoff,
   so a rate-limited run just fails slowly instead of adapting.

## Requirements

### 1. Local Resolution Cache

- Maintain a persistent cache file keyed by normalized ISBN
  (ISBN-13 preferred, falling back to the normalized input ISBN).
- Cache entry contents:
  - Resolved `BookMetadata` (on success)
  - Resolution status (`success` / `error`)
  - Error message (on failure)
  - Timestamp of last resolution attempt
- Default cache location: `~/.isbn-resolver/cache.json` (override via
  `--cache-file` flag or `cache_file` config key).
- Before resolving, look up each ISBN in the cache:
  - If present with status `success`, skip the network call and reuse the
    cached metadata.
  - If present with status `error`, still skip by default (avoid hammering
    APIs for known-bad ISBNs), but this is reconsidered under `--retry-failed`
    (see below).
  - If absent, resolve normally and write the result to the cache
    (success or failure) after the attempt.
- Cache writes should be incremental/atomic (write-through per batch or via
  a temp-file-and-rename) so a killed process doesn't corrupt the cache.

### 2. Cache-Control Flags

- `--resolve-all` — ignore the cache entirely for this run; resolve every
  input ISBN and overwrite its cache entry with the new result. Use when
  metadata may have changed upstream or the cache is suspected stale.
- `--retry-failed` — reuse cached successes but re-attempt ISBNs cached with
  status `error`. Lighter-weight than `--resolve-all` for the common case of
  "some ISBNs failed transiently last time, don't touch the rest."
- `--no-cache` — bypass cache reads and writes entirely (existing behavior),
  for one-off ad hoc runs that shouldn't pollute the cache.
- These flags are mutually exclusive with each other except `--no-cache`,
  which is orthogonal (disables caching outright, making `--resolve-all`
  / `--retry-failed` moot).
- `--verbose` output should report a cache breakdown, e.g.
  `Cache: 812 hit, 40 miss, 6 retried`.

### 3. Bounded Concurrency

- Replace the sequential resolve loop with a worker pool
  (`--concurrency N`, default e.g. 5) that resolves cache-miss ISBNs in
  parallel.
- Concurrency must be bounded and configurable — this is talking to two free
  public APIs (Open Library, Google Books), not infrastructure we control.
- Preserve input ordering in the final `results` slice for output formatting
  and sheet writing (write results into pre-sized slots by index, same as
  today, rather than appending from worker goroutines).

### 4. Rate-Limit Handling

- On HTTP 429 (or 503) from either API, apply exponential backoff with
  jitter and a small number of retries (e.g. 3) before falling back /
  failing that ISBN.
- Respect a `Retry-After` header when present instead of guessing the delay.
- Backoff/retry state should be per-worker, not global serialization — a
  429 on one ISBN's request shouldn't block unrelated in-flight requests,
  though a global rate limiter (token bucket) shared across workers is
  appropriate to avoid triggering 429s in the first place.

### 5. Sheet-Based Cache Alternative (design note, not required for v1)

- As a stateless alternative to a local cache file, the tool could read the
  existing `Status` column from the sheet's own output range before
  resolving, and skip rows already marked `Success`. This avoids needing a
  cache file to travel with the machine/CI runner running the tool, at the
  cost of an extra read call and coupling the "cache" to a specific sheet's
  output layout. Worth revisiting if the tool is run from ephemeral CI
  environments where a local cache file won't persist between runs; not
  required for the initial implementation, which assumes a persistent local
  cache file is available.

## Non-Goals

- Distributed/shared caching across multiple users or machines.
- Cache expiration/TTL (book metadata rarely changes; staleness is handled
  via `--resolve-all`, not automatic expiry).
- Batching multiple ISBNs into a single API request (neither Open Library
  nor Google Books support this for the fields we need).

## Example Usage

```bash
# Normal run: cache hits are skipped, only new ISBNs are resolved
isbn-resolver --sheets-url "URL" --sheets-range "A2:A" --cache-file ~/.isbn-resolver/cache.json

# Force re-resolution of every ISBN, refreshing the cache
isbn-resolver --sheets-url "URL" --sheets-range "A2:A" --resolve-all

# Re-attempt only previously-failed ISBNs
isbn-resolver --sheets-url "URL" --sheets-range "A2:A" --retry-failed

# Increase worker concurrency for a large first-time run
isbn-resolver --file isbns.txt --concurrency 10 --resolve-all

# Ad hoc run that shouldn't touch the cache
isbn-resolver 978-0134190440 --no-cache
```

## Expected Output (Verbose Mode)

```
Loaded cache: 812 entries (~/.isbn-resolver/cache.json)
Processing 852 valid ISBN(s) with 5 workers...
Cache: 812 hit, 40 miss
✓ Resolved ISBN 9780134190440: The Go Programming Language (via Open Library)
...
Warning: rate limited by Open Library, retrying ISBN 9780596520687 in 2.1s (attempt 1/3)
...
Summary: 848 successful, 4 failed out of 852 total
Duration: 9.2s (was ~48s before caching/concurrency on a cold run of this size)
```

## Configuration File Example

```json
{
  "cache_file": "~/.isbn-resolver/cache.json",
  "concurrency": 5,
  "resolve_all": false,
  "retry_failed": false,
  "rate_limit": {
    "max_retries": 3,
    "base_backoff": "500ms"
  }
}
```

## Testing Requirements

1. **Unit Tests**
   - Cache hit / miss / error-entry lookup logic
   - `--resolve-all`, `--retry-failed`, `--no-cache` flag interactions
   - Atomic cache write behavior (interrupted write doesn't corrupt file)
   - Worker pool preserves input ordering in output
   - Backoff/retry triggers correctly on 429/503, honors `Retry-After`

2. **Integration Tests**
   - Second run against the same input list makes zero network calls when
     nothing changed
   - `--resolve-all` re-issues network calls for all ISBNs despite a warm
     cache
   - Concurrent resolution produces identical results to sequential
     resolution for the same input set

3. **Manual Testing Checklist**
   - [ ] First run on a 500+ ISBN sheet completes noticeably faster with
         concurrency enabled
   - [ ] Second run on the same sheet with no changes completes in a few
         seconds (cache hits only)
   - [ ] `--resolve-all` visibly re-resolves everything
   - [ ] `--retry-failed` only re-attempts previously-failed ISBNs
   - [ ] Rate-limit responses trigger visible backoff/retry rather than
         immediate failure
   - [ ] Cache file survives a killed (`kill -9`) process mid-run without
         corruption

## Success Criteria

- [ ] Unchanged repeat runs make no redundant API calls
- [ ] `--resolve-all` and `--retry-failed` behave as specified
- [ ] Bounded concurrency resolves large ISBN sets faster than the current
      sequential implementation without triggering sustained rate limiting
- [ ] 429/503 responses are retried with backoff instead of failing
      immediately
- [ ] Tests achieve >80% coverage on new cache and concurrency code

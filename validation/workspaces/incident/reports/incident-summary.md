# Incident Summary

## Timeline

### Confirmed facts
- 2026-03-18T08:59:55Z — `lantern-worker` booted with `concurrency=12`.
- 2026-03-18T09:00:01Z — `lantern-api` booted in `production`, version `1.4.2`.
- 2026-03-18T09:00:10Z — Worker began polling with `batch=20` and `visibility_timeout_sec=30`.
- 2026-03-18T09:00:42Z — Worker logged `queue.retry` due to `"metrics backend slow"` (`1820ms`).
- 2026-03-18T09:01:10Z — Worker logged another retry for `"metrics backend slow"` (`1914ms`).
- 2026-03-18T09:01:12Z — API started request `req-1001` for `/api/report/daily`.
- 2026-03-18T09:01:13Z — API failed `req-1001` to upstream `queue-metrics` with `"context deadline exceeded"` and then logged `report.render fallback=true reason="metrics unavailable"`.
- 2026-03-18T09:01:40Z — Worker logged another retry for `"metrics backend slow"` (`2041ms`).
- 2026-03-18T09:02:11Z — Worker logged `queue.fail` for `job-884` with `"metrics backend timeout after 2000ms"`.
- 2026-03-18T09:02:50Z — Worker logged another retry for `"metrics backend slow"` (`1888ms`).
- 2026-03-18T09:03:40Z — API started request `req-1002` for `/api/report/daily`.
- 2026-03-18T09:03:41Z — API failed `req-1002` to upstream `queue-metrics` with `"context deadline exceeded"` and then logged `/reports/daily status=500 reason="report data incomplete"`.
- 2026-03-18T09:05:05Z — `/healthz` returned `200`.
- Deployment/config facts: `REQUEST_TIMEOUT_MS=1500`, `httpClientTimeoutMs=1000`, retry policy is `maxAttempts=1` with `backoffMs=0`, worker concurrency is `12`, queue visibility timeout is `30s`.

### Inference
- The incident window started no later than 09:00:42Z, when worker-side slowness against the metrics dependency first appeared, and was still affecting report requests at 09:03:41Z.
- Core service process health remained up during the incident, because `/healthz` succeeded even while report requests failed.

## Confirmed Root Cause

### Confirmed facts
- Both the API and worker logged failures tied to the metrics path:
  - API upstream `queue-metrics` timed out with `context deadline exceeded`.
  - Worker logged repeated `metrics backend slow` warnings and a `metrics backend timeout after 2000ms` error.
- The API is configured with a short downstream client timeout (`httpClientTimeoutMs=1000`) and no retry/backoff (`maxAttempts=1`, `backoffMs=0`).
- The report endpoint degraded from fallback behavior to a user-visible `500` due to `report data incomplete`.

### Inference / judgment
- Most likely root cause: latency or timeout failures in the shared metrics dependency path (`queue-metrics` / `metrics backend`) caused report generation to fail.
- Contributing factor: aggressive timeout/retry settings likely reduced tolerance to transient slowness and made the report path fail quickly instead of recovering.

## Recommendations

### Confirmed-fact-based actions
1. Investigate the `queue-metrics` / metrics backend around 09:00–09:04Z for latency spikes, saturation, or dependency errors.
2. Review whether `httpClientTimeoutMs=1000` and `maxAttempts=1` are appropriate for this dependency.
3. Review report rendering behavior so fallback mode does not still end in `report data incomplete` and `500` when metrics are unavailable.
4. Compare worker timeout behavior (`timeout after 2000ms`) with API timeout behavior (`1000ms`) and align them intentionally.

### Inference-driven improvements
- Consider adding bounded retries/backoff or a clearer degraded-response path for report requests.
- Consider capacity and load review for the worker/metrics path if concurrency `12` and batch size `20` are stressing the dependency.

## Open Questions
- Are `queue-metrics` and `metrics backend` the same underlying service, or two separate components in the same path?
- What changed immediately before 09:00Z (deploy, config change, traffic increase, dependency issue)?
- Why did `fallback=true` still lead to `report data incomplete` and a `500` on the later request?
- Were failures limited to the daily report path, or did other metrics-dependent endpoints/jobs degrade as well?
- Is worker load (`concurrency=12`, `batch=20`) a contributor, or was the dependency already unhealthy independent of worker pressure?

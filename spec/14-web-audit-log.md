# Web Audit Log Durability And Checkpoint Contract

## 1. Scope and managed paths

`webconsole-audit.jsonl` remains the local Web console's operator-readable audit
fact source. The permanent sidecars are `<log>.checkpoint.json` (validated tail)
and `<log>.lock` (stable cross-process advisory lock). A temporary
`<log>.pending.json` is a durable append-outcome barrier, not another audit log.

Managed files are owner-only regular files, opened with no-symlink helpers;
existing permanent files are narrowed to `0600`. A pending barrier is created
exclusively with `0600`. Any existing pending path, including an empty,
malformed, symlinked or non-regular one, blocks automatic recovery without being
parsed or followed. Config and API-key env paths must not alias any of these
paths. Parent directories are created owner-only when absent.

## 2. Startup and recovery

The `web` command binds its TCP listener before `PrepareAuditLog`, and calls
`PrepareAuditLog` before constructing `webconsole.New`, because New starts
queue workers and the stale-session reaper. A failed bind therefore exits
without audit preparation or background components; every post-bind
initialization failure (audit preparation, service construction) must close
the listener and must not print the `web console listening on` readiness
marker. The marker is published only after the listener is bound and the
service is constructed, using `listener.Addr()` so a requested port of 0
reports the OS-assigned port. Library callers must use the same preparation
before starting background mutation. Construction alone remains lazy for
library/handler tests.

Startup and recovery hold the stable audit lock. An unresolved pending barrier
is checked before opening or trusting the log and before accepting a checkpoint
fast path. The barrier requires operator reconciliation; its mere structural
validity is never treated as evidence that an operation succeeded.

Without a barrier, full validation checks every physical, newline-terminated
JSONL record, its schema/ID/type/RFC3339Nano timestamp, duplicate IDs, structural
ID byte offsets and epochs. It verifies a checkpoint's exact prefix boundary,
record count and hash chain before adopting an uncheckpointed tail. Missing
checkpoints for structural histories, truncation, file replacement, invalid
checkpoints, mixed epochs and oversized logs fail closed. Identity, size, mode,
mtime and available change metadata must remain stable across scan/probe reads.

After validation, **sync the JSONL file before publishing any checkpoint that
covers it**, then recheck the validated snapshot and path identity. A full
record readable from page cache after process death is not necessarily durable
across power loss. A recovery sync failure leaves the previous checkpoint
unchanged (or absent on first initialization). Checkpoint-file/directory sync
is not a substitute for JSONL sync.

Legacy histories are validated and assigned a random epoch without rewriting
old events. New IDs are `audit_v2_<128-bit-epoch>_<record-start-byte-offset>`.
Unmarked tails from older writers or successful new writes whose checkpoint
publication failed may be recovered after full validation and JSONL sync. New
writers interrupted while their pending marker exists are never auto-adopted.

## 3. Hash chain and checkpoint

For each complete physical record, including its newline:

```text
SHA256(previous_chain || uint64_be(len(record)) || record)
```

The checkpoint records schema, epoch, validated size/count, chain tail,
fixed-size head/tail probes, modification time, and available device/inode and
change-time metadata. It uses strict JSON and rejects unknown fields and
impossible state: invalid offsets/digests, more records than bytes, or an empty
log with non-empty count/chain/probes.

Optional metadata is capability-sensitive. Matching absence can use the fast
path; capability gain/loss forces complete verification and refresh. A present
but different identity is replacement, not an automatic re-anchor.

Publication uses an owner-only temporary file, file sync, atomic rename and
parent-directory sync. New chain state advances from the previously validated
chain plus the exact newly encoded bytes, never by blessing a freshly computed
post-append digest of arbitrary disk contents.

## 4. Append, abort and uncertain outcomes

Under the service mutex and stable cross-process lock:

1. Reject a pending barrier; validate the checkpoint or perform full recovery.
2. Assign offset IDs, encode/validate the new batch, enforce the 64 MiB limit,
   and verify the opened log is still the managed path at the expected size.
3. Exclusively create a pending marker with epoch, prior offset and batch byte
   count (no payload or credentials). Sync the marker and parent directory
   **before writing any batch bytes**. Failure leaves the log unchanged.
4. Write the complete batch and sync JSONL.
5. On a short/failed write **or failed JSONL sync**, truncate to the prior
   boundary and sync the rollback. Only a confirmed rollback can remove/sync
   the marker before returning the original error to the business caller.
6. If truncate or rollback sync is uncertain, retain the durable barrier and
   return the error and recovery location. Startup, preflight and further
   appends refuse to adopt that tail, including in a fresh process. If only
   marker cleanup fails after a confirmed rollback, report that failure; the
   marker may remain or reappear after a crash, but the aborted bytes have
   already been removed durably. A complete record is not a commit decision.
7. A complete write plus successful JSONL sync is the audit commit point.
   Remove the barrier and sync its directory; then verify size/path/probes and
   atomically advance the checkpoint. Post-commit maintenance failure must not
   return a pre-commit failure that would cause a business rollback after its
   audit event was already committed. If a marker remains, subsequent calls
   require reconciliation; if only the checkpoint is stale, recover the
   confirmed durable tail as described above.

A crash while the marker is present is deliberately conservative: the caller's
outcome cannot be inferred from offset IDs. There is no claim of a distributed
transaction across business files, audit JSONL and arbitrary process death.
This protocol prevents *automatic* adoption of a returned-failure batch whose
log rollback could not be confirmed. It does not manufacture a business commit
or abort decision during recovery.

## 5. Complexity and verification

The normal path performs fixed-size checkpoint/metadata checks, 64 KiB head and
tail probes, a small durable marker transaction, and work proportional to the
new batch. It does not decode historical JSON or allocate all historical IDs.
The extra durability barriers add constant I/O latency, not O(N) history work.

Full scans occur during preparation, checkpoint absence/mismatch, or metadata
capability changes. Full scans retain an ID set and are O(N); they are bounded
by the 64 MiB active-log cap. No fixed-interval O(N) scan is inserted into every
fixed number of appends. Operators needing continuous verification can use a
separate verifier or a controlled restart. A pending marker causes rejection,
not a scan that silently accepts the pending data.

## 6. Reconciliation and archival

Do not blindly remove a pending marker to restore availability. Stop **every**
writer sharing the session root, preserve the log/checkpoint/marker together,
and inspect the marker's prior boundary alongside business state and the
reported error. Reconcile the operation explicitly. Only after an operator
has established the outcome and preserved evidence may the barrier be removed
or the entire generation quarantined. Never discard the only recovery copy.

Normal retention does not silently delete, rotate, compress or upload audit
records. At 64 MiB, mutations fail closed. With no unresolved marker, stop all
writers, archive the JSONL and checkpoint as a pair, preserve owner-only access,
and restart with a new active generation. The stable `.lock` may remain; only
remove it while all writers are stopped. When a pending marker exists, perform
reconciliation first rather than treating routine rotation as its resolution.

## 7. Trust boundary

The advisory lock coordinates cooperating Aegis writers, not a process that
ignores the lock. Owner-only access to the log, checkpoint, marker and parent
directory is trusted. Structural IDs are collision-resistant names/recovery
markers, **not MACs**. A same-account actor or administrator able to replace
trusted files can forge them; this is not protection against that actor.

Identity/size/permission/ordinary metadata changes and altered fixed probes
invalidate the fast path. A middle-only rewrite preserving all available
metadata may evade a single bounded check. It is not blessed by a newly
recomputed chain: a subsequent full prefix-chain check fails. Successful fsync
and atomic-rename durability still depend on the underlying filesystem/storage
honoring those operations. Tests inject errors at deterministic seams; they do
not claim physical power-loss certification.

## 8. Regression requirements

Existing tests retain schema/duplicate/offset/epoch validation, truncation,
replacement, no-symlink/regular-file/permission checks, legacy migration,
checkpoint integrity and zero history decodes on normal appends.

`audit_durability_test.go` additionally covers single/multi-event JSONL sync
failure, successful rollback and sync ordering, failed truncate/rollback sync,
persistent rejection in a fresh process, marker creation/cleanup failures,
malformed pending paths, new/legacy/tail recovery ordering, recovery-sync failure
without checkpoint advancement, committed-but-uncheckpointed tail recovery,
and cooperating writes from multiple actual local processes.

# Web Audit Log Durability And Checkpoint Contract

## 1. Scope

The local Web console records security-sensitive mutations in
`webconsole-audit.jsonl`. The JSONL file remains the operator-readable source
of audit facts. A checkpoint and a lock are implementation sidecars; neither is
a second audit history.

Managed paths derived from the JSONL path are:

- `<log>.checkpoint.json`: atomically published validated-tail checkpoint.
- `<log>.lock`: stable cross-process advisory lock for cooperating Web console
  processes.

All three paths use owner-only permissions and no-symlink file helpers.

## 2. Startup and recovery

Before the real `aegis-agent web` command begins listening, the service must
perform a complete validation while holding the cross-process audit lock.
Construction through `webconsole.New` remains lazy so library and handler tests
do not create runtime files merely by constructing a service.

Full validation must:

- read every physical JSONL record with a bounded record size;
- require newline termination;
- validate JSON, schema version, ID, event type, and RFC3339Nano time;
- reject duplicate IDs;
- validate the exact byte offset and epoch of structural IDs;
- verify an existing checkpoint's prefix byte boundary, record count, and hash
  chain before adopting any durable uncheckpointed tail;
- reject replacement, truncation, malformed checkpoints, mixed structural-ID
  epochs, and active logs above the retention limit;
- reject a missing checkpoint when the log already contains structural IDs, so
  deleting the durable anchor cannot silently re-trust a rewritten v2 history.

Legacy event IDs are accepted during migration. After the first validated
checkpoint is created, new events use structural IDs of the form
`audit_v2_<128-bit-epoch>_<record-start-byte-offset>` so steady-state collision
checking does not require an in-memory set of all historical IDs.

## 3. Hash chain and checkpoint

For each complete physical JSONL record `record`, the chain advances as:

```text
SHA256(previous_chain || uint64_be(len(record)) || record)
```

The checkpoint records at least:

- schema version and structural-ID epoch;
- file identity where the host exposes it;
- validated byte size and record count;
- tail hash-chain value;
- fixed-size head and tail content probes;
- modification/change metadata where available.

The checkpoint is strict JSON with unknown fields rejected. Publication uses an
owner-only temporary file, file sync, atomic rename, and parent-directory sync.
A checkpoint never derives a new trusted chain by hashing the post-append file.
It advances the previously validated chain only with the exact bytes the process
encoded for the new batch.

## 4. Append ordering

A cooperating append holds both the service mutex and the cross-process file
lock. The order is:

1. Validate the current checkpoint against file identity, size, metadata, and
   bounded head/tail probes, or fall back to a complete validation.
2. Assign structural IDs from the checkpoint epoch and exact starting offsets.
3. Encode and validate the complete new batch in memory.
4. Enforce the active-log retention limit.
5. Append the batch and sync the JSONL file.
6. Verify that the opened file is still the regular file at the managed path and
   that its size is exactly the expected size.
7. Publish the next checkpoint atomically.

Once step 5 succeeds, the audit event is durable. A checkpoint-publication
failure must not make the caller roll back a business mutation after its audit
event is already durable. The previous checkpoint remains the recovery anchor;
the next startup or append validates its prefix and adopts the structural,
newline-terminated durable tail before publishing a replacement checkpoint.

## 5. Fast path and full verification

After a trusted startup or recovery validation, a steady-state append does not
JSON-decode historical records and uses bounded working memory independent of
retained record count. It checks the durable checkpoint, file identity and
metadata, and fixed-size head/tail probes, then advances the hash chain from the
exact intended new bytes.

A complete validation is forced:

- before the actual Web server starts listening;
- when the checkpoint is absent; or
- when file identity, size, metadata, or bounded probes no longer match the
  checkpoint.

Complete validation is deliberately not scheduled after a fixed number of
appends: a fixed-interval O(N) scan would preserve O(N²) cumulative work and
would not satisfy the bounded steady-state append contract. The active JSONL
file is capped at 64 MiB, so startup and recovery validation have a fixed upper
bound. Operators that need continuous verification of a running process must
use an external verifier or restart the Web console to force a complete scan;
that work must not be hidden inside every fixed number of mutations.

## 6. Retention and archival

The implementation does not silently delete or automatically rotate audit
records. The active log has a hard 64 MiB limit. At the limit, audited mutations
fail closed with an operator instruction.

To archive safely:

1. Stop every Web console process using the session root.
2. Move `webconsole-audit.jsonl` and its `.checkpoint.json` sidecar together to
   the archival destination.
3. Preserve owner-only access and the pair's relative association.
4. The `.lock` file may remain in place or be removed only while every process
   is stopped.
5. Restart the Web console; startup creates and fully validates a new empty
   active log and checkpoint before listening.

Automatic retention, deletion, compression, or upload is outside this contract.

## 7. Trust boundary

The file lock coordinates Aegis processes that use this implementation. It
cannot stop an unrelated process that deliberately ignores advisory locking.
Replacement, truncation, size changes, ordinary metadata changes, and changes in
the fixed head/tail probes invalidate the fast path immediately.

A non-cooperating writer that can rewrite only the unprobed middle of a file
while also preserving all available identity and metadata signals may not be
detected by that single fast-path check. Such a rewrite is not incorporated into
or blessed by a newly recomputed trusted chain: the next startup or recovery
validation compares the complete checkpoint prefix chain and fails closed. The
log is an integrity-checked local audit trail, not a cryptographic defense
against an administrator who can rewrite the log and checkpoint together.

## 8. Required validation

Regression coverage must include:

- zero historical JSON decodes on a checkpoint fast-path append;
- durable uncheckpointed-tail recovery after checkpoint publication failure;
- startup detection of a same-size historical rewrite;
- truncation and file replacement rejection;
- checkpoint epoch mismatch against structural history;
- config and API-key env paths that alias checkpoint or lock sidecars;
- concurrent cooperating writers;
- legacy-ID migration and structural-ID offset/epoch validation;
- malformed and oversized checkpoint/log rejection;
- append benchmarks at 1,000, 10,000, and 100,000 retained records.

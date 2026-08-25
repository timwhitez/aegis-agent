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

All three managed paths must be regular files with owner-only `0600`
permissions. Existing files are narrowed to that mode before use; symlinks,
directories, FIFOs, devices, sockets, and other non-regular paths are rejected.
The parent directory is created owner-only when absent.

## 2. Startup and recovery

The CLI prepares the audit log before constructing the Web service. This order
is material: `webconsole.New` starts queue workers and the stale-session reaper,
and those background components may mutate durable state before the HTTP
listener is opened. Audit validation therefore happens before worker/reaper
construction as well as before network listening.

Construction through `webconsole.New` remains lazy for library callers and
handler tests. Production callers that can start background mutation must call
`webconsole.PrepareAuditLog` first.

Startup preparation holds the cross-process audit lock and performs a complete
validation. Full validation must:

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
  deleting the durable anchor cannot silently re-trust a rewritten v2 history;
- verify that identity, size, mode, modification time, and any available change
  timestamp remain stable across the scan and probe capture.

Legacy event IDs are accepted during migration. After the first validated
checkpoint is created, new events use structural IDs of the form
`audit_v2_<128-bit-epoch>_<record-start-byte-offset>` so steady-state collision
checking does not require an in-memory set of all historical IDs.

## 3. Hash chain and checkpoint

For each complete physical JSONL record `record`, the chain advances as:

```text
SHA256(previous_chain || uint64_be(len(record)) || record)
```

The checkpoint records:

- schema version and structural-ID epoch;
- file identity when the filesystem exposes a stable device/inode identity;
- validated byte size and record count;
- tail hash-chain value;
- fixed-size head and tail content probes;
- modification time and change timestamp when available.

Optional host metadata is capability-sensitive. A missing file-identity or
change-timestamp field is a valid checkpoint state only while the current
filesystem exposes the same absence. Capability gain or loss forces a complete
scan and checkpoint refresh instead of permanently disabling the fast path.

The checkpoint is strict JSON with unknown fields rejected. It also rejects
impossible state, including an invalid tail offset, more records than bytes, or
an empty file with non-empty chain/count/digest state. Publication uses an
owner-only temporary file, file sync, atomic rename, and parent-directory sync.
A checkpoint never derives a new trusted chain by hashing the post-append file.
It advances the previously validated chain only with the exact bytes the process
encoded for the new batch.

## 4. Append ordering

A cooperating append holds both the service mutex and the cross-process file
lock. The order is:

1. Validate the current checkpoint against file identity, size, mode, metadata,
   and bounded head/tail probes, or fall back to a complete validation.
2. Assign structural IDs from the checkpoint epoch and exact starting offsets.
3. Encode and validate the complete new batch in memory.
4. Enforce the active-log retention limit.
5. Reconfirm that the opened regular log is still the managed path.
6. Append the complete batch and sync the JSONL file.
7. Best-effort verify the exact post-append file and capture probes.
8. Publish the next checkpoint atomically.

A failed or short write is truncated back to the old byte boundary and that
rollback is synced before the write failure is returned.

A complete write followed by a successful JSONL `fsync` is the audit commit
point. From that point onward a checkpoint/probe failure must not make the
caller roll back the business mutation: doing so would leave a durable audit
event that describes an action the caller subsequently undid. The previous
checkpoint remains the recovery anchor, and the next startup or append performs
a complete validation of its prefix plus the structural, newline-terminated
durable tail before publishing a replacement checkpoint.

This is an explicit atomicity tradeoff. If a non-cooperating process replaces or
corrupts the managed path after the audit commit point, the current business
operation is not retroactively rolled back; later audited mutations fail closed
when recovery cannot validate the old checkpoint and tail.

## 5. Fast path and full verification

After trusted startup or recovery, a steady-state append does not JSON-decode
historical records and uses bounded working memory independent of retained
record count. It checks the durable checkpoint, regular-file identity and mode,
size and metadata, and fixed-size head/tail probes, then advances the hash chain
from the exact intended new bytes. File metadata is rechecked after probe reads
so a file changing during the fast-path check cannot be accepted as stable.

A complete validation is forced:

- before background Web components and the HTTP server are constructed;
- when the checkpoint is absent;
- when file identity capability changes; or
- when identity, size, mode, metadata, or bounded probes no longer match the
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
   active log and checkpoint before constructing background workers or
   listening.

Automatic retention, deletion, compression, or upload is outside this contract.

## 7. Trust boundary

The file lock coordinates Aegis processes that use this implementation. It
cannot stop an unrelated process that deliberately ignores advisory locking.
Replacement, truncation, size changes, permission changes, ordinary metadata
changes, and changes in the fixed head/tail probes invalidate the fast path.

Structural IDs are collision-resistant names and recovery markers, not message
authentication codes. The epoch and byte offsets are visible in the log. Tail
recovery therefore assumes that owner-only write access to the managed JSONL,
checkpoint, and their parent directory is trusted. Any process that has
permission to write or replace those files can synthesize a structurally valid
event or rewrite both log and checkpoint; the design does not claim to defend
against that actor.

A non-cooperating writer that can rewrite only the unprobed middle of a file
while also preserving every available identity and metadata signal may not be
detected by that single fast-path check. Such a rewrite is not incorporated into
or blessed by a newly recomputed trusted chain: the next startup or recovery
validation compares the complete checkpoint prefix chain and fails closed. The
log is an integrity-checked local audit trail, not a cryptographic defense
against an administrator or same-account process that can rewrite its trusted
files.

## 8. Required validation

Regression coverage must include:

- audit preparation rejecting malformed history before Web service background
  construction;
- zero historical JSON decodes on a checkpoint fast-path append;
- durable uncheckpointed-tail recovery after checkpoint publication failure;
- startup detection of a same-size historical rewrite;
- truncation and file replacement rejection;
- checkpoint epoch mismatch against structural history;
- config and API-key env paths that alias checkpoint or lock sidecars;
- non-regular log/checkpoint/lock rejection without FIFO blocking;
- hardening pre-existing managed files to `0600`;
- optional file-identity/change-stamp capability matching;
- impossible checkpoint-state rejection;
- concurrent cooperating writers;
- legacy-ID migration and structural-ID offset/epoch validation;
- malformed and oversized checkpoint/log rejection;
- append benchmarks at 1,000, 10,000, and 100,000 retained records.

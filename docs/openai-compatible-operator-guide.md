# OpenAI-Compatible Operator Guide

## Scope

This guide is the operator-facing recipe for running `go-cli-agent` against an OpenAI-compatible deployment that speaks the Responses wire shape.

It answers four concrete questions:

1. Which transport/auth contract must the gateway satisfy?
2. Which provider settings does `go-cli-agent` actually send?
3. Which commands should an operator run before a live task matrix?
4. Where should the operator verify the effective settings after a run?

## Required contract

- Base URL must expose a `/v1` root that serves `POST /responses`.
- Authentication is bearer-token based through `Authorization: Bearer <token>`.
- The provider entry must use `wire_api: responses`.
- `openai-compatible` reuses the OpenAI Responses adapter; it is not a separate replay path.

## Recommended config

```yaml
default_provider: openai-compatible

providers:
  openai-compatible:
    api_key_env: OPENAI_API_KEY
    base_url: http://64.186.236.156:24634/v1
    model: gpt-5.4
    timeout_sec: 240
    wire_api: responses
    store: false
    send_metadata: false
    retry:
      max_attempts: 2
      base_delay_ms: 1000
      retry_5xx: true
      retry_transport: true
```

## Setting semantics

- `store`
  - Effective default is `false` for `openai` and `openai-compatible`.
  - Reason: the durable source of truth is the local session directory, not remote provider storage.
- `send_metadata`
  - Effective default is `true`.
  - Set it to `false` only when the target gateway rejects the `metadata` field.
- `retry`
  - Retries are adapter-level transport retries.
  - They are meant for `429`, `5xx`, and transport timeout/unavailable cases only.
  - Authentication errors, request-shape errors, and response-parse errors are not safe retry candidates.

## Preflight commands

Build and local test surface:

```sh
./build.sh
./test.sh
```

Operator config/self-check:

```sh
./bin/go-cli-agent doctor \
  --config validation/config.openai-compatible.yaml \
  --provider openai-compatible \
  --json
```

Minimal live probe:

```sh
./bin/go-cli-agent probe-provider \
  --config validation/config.openai-compatible.yaml \
  --provider openai-compatible \
  --json
```

Full live smoke:

```sh
GO_CLI_AGENT_LIVE_RESPONSES_URL=http://64.186.236.156:24634/v1 \
./live_smoke.sh
```

The same `GO_CLI_AGENT_LIVE_RESPONSES_URL` override is now honored by:

- `validation/run_openai_compatible_acceptance_stack.sh`
- `validation/run_round31_complex_real_matrix.sh`
- `validation/run_experimental_webconsole_followup_validation.sh`

## What to verify in doctor output

For `provider.config`, confirm:

- `base_url`
- `api_key_env`
- `wire_api`
- `timeout_sec`
- `store`
- `store_source`
- `send_metadata`
- `send_metadata_source`
- `retry_policy.max_attempts`
- `retry_policy.base_delay_ms`
- `retry_policy.retry_429`
- `retry_policy.retry_5xx`
- `retry_policy.retry_transport`

For `provider.api_key_env`, confirm:

- `present: true`

For `provider.probe`, confirm:

- `stop_reason`
- `tool_call_names`
- `finish_message`

## What to verify in durable session output

After a live run, inspect the session directory and confirm:

- `session.json`
  - `provider`
  - `model`
  - `provider_options.store`
  - `provider_options.send_metadata`
  - `provider_options.retry_policy`
- `events.jsonl`
  - `provider.retry` appears only when the gateway actually retried
- `messages.jsonl`
  - local session history remains the durable execution source

## Failure triage

- `auth_error`
  - Usually bad token, wrong env var, or a gateway that expects a different auth scheme.
- `invalid_request`
  - Usually wrong `wire_api`, unsupported field handling, or an incompatible endpoint shape.
- `upstream_timeout`
  - Usually gateway/model latency or a transport timeout worth retrying within the configured budget.
- `upstream_unavailable`
  - Usually host, route, or transient gateway availability problems.

## Current operational stance

- The recommended validation path for this repo is still `doctor` -> `probe-provider` -> `live_smoke.sh` -> full matrix script.
- After `live_smoke.sh`, the current one-command repo-owned acceptance entry is `validation/run_openai_compatible_acceptance_stack.sh`; it now runs a bundle-level `probe-provider` gate first, then chains the full matrix and the focused webconsole follow-up into one bundle summary while preserving each child run directory.
- If the acceptance-stack preflight probe still fails after its bounded retries, the bundle writes `ABORTED.md`, skips both live phases, and preserves the probe evidence under `raw/preflight-probe-attempt*.json` plus `notes/preflight-index.tsv`.
- If the change touches `experimental web`, resume retry recovery, or queue/background notifications, follow the full matrix with `validation/run_experimental_webconsole_followup_validation.sh`; it adds embedded-asset checks, a real headless browser interaction smoke, durable retry-policy proof, queue notification dedup evidence, and operator-surface checks such as session sidebar filtering/reveal, queue quick-filter chips, and timeline event filtering.
- In the current stable proof run `validation/runs/2026-03-27-openai-compatible-gpt-5.4-round54e-experimental-webconsole-followup-stable-proof/`, the retry-resume session still ended in `awaiting_input` after two bounded finish nudges. Treat that as a non-blocking completion quirk, not as a retry-policy failure: the proof threshold is that durable session metadata still shows the original retry budget and the resumed turn emits a real `provider.retry`.
- The webconsole follow-up script needs `node` plus a local Chrome/Chromium binary; set `CHROME_BIN` if the browser is not available on the default path.
- If a non-official gateway rejects `metadata`, set `send_metadata: false` and verify that `doctor` reflects the effective value before starting a large run.
- If a run needs postmortem traceability, rely on local session artifacts first, not remote provider dashboards.

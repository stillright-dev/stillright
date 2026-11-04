# report.json schema

`stillright run --report-json report.json` writes one JSON object per run of
one scenario. It is meant for CI, dashboards and other tools.

## Versioning

The `schema` field identifies the format. It is currently
**`stillright.report/v1`**.

Within a version:

- fields are never removed, renamed or given a new meaning;
- new fields may be added, so ignore keys you don't know.

Any breaking change bumps the version (`stillright.report/v2`).

Conventions:

- Keys are `snake_case`.
- Durations are in milliseconds (`*_ms`).
- Timestamps are RFC 3339 in UTC.
- Fields documented as "or null" are always present.
- Fields documented as "omitted when empty" may be missing.

## Top level

| Field | Type | Meaning |
| --- | --- | --- |
| `schema` | string | `"stillright.report/v1"` |
| `stillright_version` | string | version of the binary that wrote the report |
| `generated_at` | string | when the report was written (RFC 3339, UTC) |
| `scenario` | object | `name` and `path` (as given on the command line). `name` is empty if the file could not be loaded |
| `status` | string | `PASS`, `FAIL` or `ERROR`, matching `exit_code` 0, 1 and 2 |
| `exit_code` | integer | the process exit code |
| `error` | string, omitted when empty | why nothing could run: invalid scenario, database unreachable, proxy port busy… |
| `interrupted` | boolean | the run was stopped (Ctrl-C) before finishing |
| `seed` | string | the seed, as a decimal string so 64-bit values survive JSON parsers |
| `mode` | string | `explore` (generated sequences), `replay` (`--replay N`) or `sequence` (`--sequence`) |
| `attempts` | integer | retries per sequence when replaying and shrinking (`--attempts`) |
| `perturb` | object | see below |
| `runs` | object | `planned`, `executed`, `passed`, `failed`, `errored` (counts of runs) |
| `checks` | array of Check | one per invariant, then one per effect, in declaration order |
| `stopped_at` | Run or null | the first run that did not pass |
| `proxy` | object or null | `listen`, `calls` (total recorded), `hosts` (every host seen); null when the scenario has no effects |
| `warnings` | array of string | results that may be vacuous, for example an effects proxy that saw no calls. Treat them as "not verified" |

`perturb` has these fields:

- `enabled`: whether any perturbation is configured.
- `duplicate`, `concurrency`: integers, 1 when not set.
- `reorder`: boolean.
- `delay_ms`: `[min, max]`.
- `settle_ms`: integer.

## Check

| Field | Type | Meaning |
| --- | --- | --- |
| `name` | string | as declared |
| `kind` | string | `invariant` or `effect` |
| `status` | string | `PASS`, `FAIL` (failed in at least one run) or `ERROR` (could not be checked, never failed) |
| `failed_runs` | integer | runs in which it failed |
| `errored_runs` | integer | runs in which it could not be checked |
| `error` | string, omitted when empty | why it could not be checked, the first time |
| `first_failure` | Failure or null | the first run where it failed |
| `shrunk` | Shrunk or null | the reduction of `first_failure`; null when shrinking was off or the check never failed |
| `reproduce` | array of string | command lines that rerun the failure; empty when it never failed |

A check can be `FAIL` and still have `errored_runs > 0`. The top-level
`status` is then `ERROR`, because some runs were not verified.

### Failure

| Field | Type | Meaning |
| --- | --- | --- |
| `sequence` | Sequence | the sequence of that run |
| `detail` | string, omitted when empty | for effects: `"2 matching calls, expected <= 1"` |
| `evidence` | Evidence | rows the invariant returned, or the matching calls for an effect |

### Shrunk

| Field | Type | Meaning |
| --- | --- | --- |
| `reproducible` | boolean | false when the original failure did not happen again in `attempts` runs. Nothing was shrunk and the following fields are null or zero |
| `sequence` | Sequence or null | the smallest sequence found |
| `reproduced` | integer | failures out of `attempts` confirmation runs of that sequence. Below `attempts` means it is timing-dependent |
| `attempts` | integer | confirmation runs |
| `candidates_tried` | integer | sequences tried while shrinking |
| `budget_exhausted` | boolean | shrinking stopped early, so the result may not be minimal |
| `detail` | string, omitted when empty | as in Failure, from a confirmation run |
| `evidence` | Evidence or null | from a failing confirmation run; null if none failed |

### Sequence

| Field | Type | Meaning |
| --- | --- | --- |
| `index` | integer or null | the sequence number under `seed`; null for shrunk or hand-written sequences |
| `text` | string | the notation the terminal prints and `--sequence` accepts, e.g. `"[pago ‖ pago] → [estornado]"` |
| `batches` | array of arrays of `{event, delay_ms}` | batches run in order; sends inside a batch are released together |

### Evidence

| Field | Type | Meaning |
| --- | --- | --- |
| `columns` | array of string | column names (for effects: `method`, `host`, `path`, `body`) |
| `rows` | array of arrays of string or null | at most 10 rows. PostgreSQL values use its text format and `null` is SQL NULL. MongoDB columns are top-level document fields, values are relaxed Extended JSON (strings as-is), and `null` is a missing field |
| `total` | integer | how many rows there were in total |

### Run

| Field | Type | Meaning |
| --- | --- | --- |
| `sequence` | Sequence | |
| `deliveries` | array | `{event, method, path, batch, delay_ms, status, duration_ms, error?}`. `status` is 0 when the app did not answer. `batch` starts at 0 |
| `error` | string, omitted when empty | why the run could not be verified |

## Example

The example is shortened.

```json
{
  "schema": "stillright.report/v1",
  "stillright_version": "0.1.0",
  "generated_at": "2026-11-04T00:14:14Z",
  "scenario": { "name": "pagamento-confirmado", "path": "stillright/pagamento.yaml" },
  "status": "FAIL",
  "exit_code": 1,
  "interrupted": false,
  "seed": "8812",
  "mode": "explore",
  "attempts": 5,
  "perturb": { "enabled": true, "duplicate": 3, "concurrency": 4, "reorder": true, "delay_ms": [0, 50], "settle_ms": 0 },
  "runs": { "planned": 100, "executed": 100, "passed": 32, "failed": 68, "errored": 0 },
  "checks": [
    {
      "name": "uma assinatura ativa por cliente",
      "kind": "invariant",
      "status": "FAIL",
      "failed_runs": 12,
      "errored_runs": 0,
      "first_failure": {
        "sequence": { "index": 6, "text": "[pago+24ms ‖ estornado+21ms ‖ pago+14ms]", "batches": [[{ "event": "pago", "delay_ms": 24 }, "…"]] },
        "evidence": { "columns": ["cliente_id", "count"], "rows": [["42", "3"]], "total": 1 }
      },
      "shrunk": {
        "reproducible": true,
        "sequence": { "index": null, "text": "[pago ‖ pago]", "batches": [[{ "event": "pago", "delay_ms": 0 }, { "event": "pago", "delay_ms": 0 }]] },
        "reproduced": 5, "attempts": 5, "candidates_tried": 9, "budget_exhausted": false,
        "evidence": { "columns": ["cliente_id", "count"], "rows": [["42", "2"]], "total": 1 }
      },
      "reproduce": [
        "stillright run stillright/pagamento.yaml --seed 8812 --replay 6",
        "stillright run stillright/pagamento.yaml --sequence '[pago ‖ pago]'"
      ]
    }
  ],
  "stopped_at": null,
  "proxy": null,
  "warnings": []
}
```

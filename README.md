<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/brand/stillright-logo-white.svg">
    <img src="docs/brand/stillright-logo-black.svg" alt="Stillright" width="320">
  </picture>
</p>

<p align="center"><strong>Still right when things go wrong.</strong></p>

Stillright tests whether your application's real behavior stays correct when
its events are **duplicated, delivered concurrently or out of order**. You
write business rules as SQL **invariants**. Stillright delivers your webhooks
the way real gateways do and checks those rules after every run.

It is black-box and works with any language: it talks to your app over HTTP
and to its database over SQL. You don't change a line of application code.

## Why

A payment gateway sends `payment.confirmed`. Your handler checks whether the
customer already has an active subscription, finds none and creates one.
Tests pass and code review passes.

Then the gateway retries, because your 200 arrived a little late. The retry
lands **while the first delivery is still running**. Both checks see "no
subscription" and both insert one. The customer now has two subscriptions,
two payment records and two welcome e-mails. Next month they get billed
twice.

Gateways document this: deliveries are *at least once*, *in no particular
order*. Almost nobody tests for it, because a normal test sends each event
once, in order, and waits for the answer.

Stillright sends them the other way:

```
$ stillright run stillright/assinatura-duplicada.yaml
FAIL   uma assinatura ativa por cliente
       failed in 2 of 50 runs; first in sequence 26: [pago+15ms ‖ pago+16ms]
       smallest sequence: [pago ‖ pago]   (2 simultaneous deliveries; reproduced 5/5)
       evidence: cliente_id=42 ativas=2
       reproduce: stillright run stillright/assinatura-duplicada.yaml --seed 8812 --replay 26
                  stillright run stillright/assinatura-duplicada.yaml --sequence '[pago ‖ pago]'
```

## How it works

1. **Reset.** Truncate the tables you list and run your seed SQL.
2. **Deliver.** Send the scenario's events in a generated sequence. Events
   are duplicated (up to `duplicate` times), sent in concurrent batches
   (released together behind a barrier), reordered (violating `after`) and
   delayed. Sequence 0 is always the plain declared order.
3. **Check.** Run every invariant, a SQL query that must return **zero
   rows**, in a read-only transaction. Any rows returned are the evidence.
   Optionally, check the app's outbound calls (e-mails, API calls) recorded
   by a local stub proxy.
4. **Shrink.** When something breaks, reduce the sequence to the smallest one
   that still breaks it, preferring sequential over concurrent. Then print
   the commands that reproduce it.

Everything is driven by a seed (`--seed`). The same seed generates the same
sequences. If anything cannot be verified (database down, app not
answering, bad SQL), the result is `ERROR` with the reason, never `PASS`.

## Install

Stillright is a single Go binary. With Go 1.25+:

```sh
go install github.com/stillright-dev/stillright/cmd/stillright@latest
```

or from a clone:

```sh
git clone https://github.com/stillright-dev/stillright && cd stillright
go build -o stillright ./cmd/stillright
```

You also need PostgreSQL. Use a **disposable** database: Stillright
truncates the tables you list before every run.

## Quickstart

[`examples/assinaturas`](examples/assinaturas) is a small Node webhook
(Express + pg) with three real, common bugs, three scenarios that find them,
and a fixed version.

```sh
cd examples/assinaturas
createdb assinaturas && export DATABASE_URL=postgres://localhost/assinaturas
npm install && npm run db:setup
EMAIL_API_URL=http://127.0.0.1:8899/api.email.local npm start &

stillright run stillright/assinatura-duplicada.yaml --keep-going
stillright run stillright/estorno-fora-de-ordem.yaml
stillright run stillright/email-boas-vindas.yaml
```

Its [README](examples/assinaturas/README.md) walks through each failure and
the fix, with the output before and after.

## Scenarios

A scenario is a YAML file kept in your repository, usually under
`stillright/`:

```yaml
version: 1
name: pagamento-confirmado
target:
  base_url: http://localhost:3000
  database_url: ${DATABASE_URL}        # ${VAR} is read from the environment

setup:
  sql: fixtures/seed.sql               # optional; runs before every sequence
  reset: truncate                      # truncate | none
  reset_tables: [assinaturas, pagamentos, emails]

events:
  - id: pago
    request:
      method: POST
      path: /webhooks/pagamento
      headers: { X-Signature: "${WEBHOOK_SIGNATURE}" }
      body_file: fixtures/payment_received.json   # .json gets Content-Type: application/json
  - id: estornado
    request:
      method: POST
      path: /webhooks/pagamento
      body_file: fixtures/payment_refunded.json
    after: pago                        # business order; reorder may violate it
    accept_status: [200, 409]          # optional; default: anything below 500

perturb:
  duplicate: 3                         # each event delivered 1..3 times
  concurrency: 5                       # up to 5 deliveries released together
  reorder: true                        # allow breaking `after`
  delay_ms: [0, 250]                   # random delay per delivery
runs: 200                              # sequences to generate (default 100)
settle_ms: 0                           # wait for background work before checking

invariants:
  - name: uma assinatura ativa por cliente
    sql: |
      SELECT cliente_id, count(*) FROM assinaturas
      WHERE status = 'ativa' GROUP BY 1 HAVING count(*) > 1
  - name: pagamento estornado não deixa assinatura ativa
    sql: |
      SELECT a.id FROM assinaturas a JOIN pagamentos p ON p.assinatura_id = a.id
      WHERE p.status = 'estornado' AND a.status = 'ativa'

proxy:                                 # optional; see docs/effects.md
  listen: 127.0.0.1:8899
  stubs:
    - { host: api.email.local, status: 202, body: '{"queued": true}' }
effects:
  - name: no máximo um e-mail de boas-vindas
    assert: count(host == "api.email.local" && path == "/send") <= 1
```

Relative paths resolve against the scenario file. Every mistake is reported
with its line and field, all at once:

```
stillright/pagamento.yaml:23: events[1].after: unknown event "pagoo"
stillright/pagamento.yaml:31: perturb.delay_ms: must satisfy 0 <= min <= max (got [300, 100])
```

**Writing invariants.** Ask "what must never be true?" and select it:

- two active subscriptions for one customer;
- a payment recorded twice;
- money refunded but access still granted;
- stock below zero.

Return the columns that help you debug. They are printed as evidence.

## Command line

```
stillright run [flags] <scenario.yaml>
```

| Flag | |
| --- | --- |
| `--seed N` | seed for generating sequences (default: random, always printed) |
| `--runs N` | sequences to run, overriding `runs` |
| `--keep-going` | run every sequence instead of stopping at the first failure |
| `--replay N` | run only sequence N of `--seed` |
| `--sequence '[a ‖ a] → [b]'` | run a hand-written sequence (`\|` and `->` work too) |
| `--attempts N` | retries per sequence when replaying and shrinking (default 5) |
| `--no-shrink` | report failures as found |
| `--http-timeout D` | timeout per request to the app (default 10s) |
| `--report-json FILE` | machine-readable report ([schema](docs/report-schema.md)) |
| `--report-html FILE` | self-contained HTML report |
| `--summary FILE` | append a Markdown summary (for `$GITHUB_STEP_SUMMARY`) |

Exit codes:

| Code | Meaning |
| --- | --- |
| `0` | every invariant and effect passed |
| `1` | something failed |
| `2` | something could not be verified: invalid scenario, database or app unreachable, a 5xx, bad SQL, an interrupted run |

## Reading a failure

```
FAIL   uma assinatura ativa por cliente
       failed in 2 of 50 runs; first in sequence 26: [pago+15ms ‖ pago+16ms]
       smallest sequence: [pago ‖ pago]   (2 simultaneous deliveries; reproduced 5/5)
       evidence: cliente_id=42 ativas=2
```

How to read the sequence notation:

- `[a ‖ b]` is one batch, delivered concurrently.
- `→` separates batches, which run one after another.
- `+15ms` is the delay before that delivery.

`reproduced 5/5` says how reliably the smallest sequence fails. Races
depend on timing, so a seed reproduces the **sequence** exactly but not the
**timing**. Stillright retries each sequence (`--attempts`) before deciding
that it passes, and tells you the rate it observed.

## Effects, reports and CI

- **[Effects](docs/effects.md):** check outbound calls (e-mails, payment
  APIs) through a local proxy that records them and answers with stubs.
- **[Reports](docs/report-schema.md):** `report.json` (versioned schema),
  `report.html` (a single file that works on a phone) and a Markdown
  summary.
- **[CI](docs/ci.md):** a GitHub Action that runs every scenario in a folder,
  publishes the job summary and uploads the reports.

```yaml
- uses: stillright-dev/stillright@main
  with:
    scenarios: stillright
```

## Limitations of v0.1

Stillright v0.1 is useful, but it does not do everything:

- **PostgreSQL and HTTP only.** No MySQL and no queues (Kafka, SQS…). Events
  must be HTTP requests to your app.
- **Black-box only.** It changes what your app *receives*: duplicates,
  concurrency, order and delays. It does not crash your process, cut its
  network or slow its database. Bugs that need those stay hidden.
- **Event bodies are static.** Every delivery of an event sends the same
  body. That models redelivery, which carries the same event id. It cannot
  model many distinct events yet.
- **Timing is not controlled.** Seeds reproduce sequences, not thread
  scheduling. A race that needs a very narrow window may fail only
  sometimes, or not at all, in a given run. More `runs` and `--attempts`
  help, but nothing guarantees it.
- **Shrinking is greedy and bounded** (60 candidates per failure). The result
  is small and confirmed to fail, but not always the absolute minimum.
- **`reset: truncate` really truncates** (`RESTART IDENTITY CASCADE`). Never
  point a scenario at a database you care about.
- **Effects:**
  - no HTTPS interception (HTTPS calls are recorded by host only);
  - one proxy shared by all runs;
  - Node's built-in `fetch` needs `NODE_USE_ENV_PROXY=1` or a direct URL.

  See [docs/effects.md](docs/effects.md).
- **One scenario per invocation.** The GitHub Action loops over a folder.
- Tested on macOS and Linux (CI). Windows is untested.

Not in v0.1, by design: a web dashboard, history across versions, in-process
fault injection, HTTPS MITM, MySQL, message queues and an MCP server for
coding agents. The code keeps clean interfaces where these would go
(`Target`, `Store`, `effects.Recorder`).

## License

[Apache-2.0](LICENSE)

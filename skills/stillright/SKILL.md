---
name: stillright
description: Test that an app's business rules still hold when its webhooks or HTTP events are duplicated, concurrent or out of order, using the stillright CLI (PostgreSQL or MongoDB). Use when the user wants to test webhook idempotency, double charges, race conditions in payment/subscription/order handlers, out-of-order events (refund before payment), duplicate e-mails or other side effects; when they mention stillright, invariants or stillright/*.yaml scenarios; or when you have just written or changed a webhook or event handler backed by PostgreSQL or MongoDB and should prove it is safe under redelivery. Covers writing scenarios and invariants from the app's code, running them, reading report.json, fixing what breaks and adding the GitHub Action.
---

# Stillright

Stillright delivers an app's events the way real gateways do: duplicated,
concurrent and reordered. After every run it checks **invariants**, which
are queries that must return nothing: SQL for PostgreSQL, or an
aggregation pipeline or find filter for MongoDB. When one breaks, it shrinks the
failure to the smallest sequence that still breaks it, such as
`[pago ‖ pago]` or `[estornado] → [pago]`.

It is black-box. It talks to the app over HTTP and to its database
directly, and the app's code needs no changes to be tested.

## Safety first

Stillright **empties the tables in `reset_tables` (PostgreSQL) or the
collections in `reset_collections` (MongoDB) before every run**.
Before running anything:

1. Find the `DATABASE_URL` the scenario uses (`target.database_url`, usually
   `${DATABASE_URL}`) and **tell the user which host and database it points
   to**. Never run against production or any database with data someone
   cares about. When in doubt, ask.
2. Stillright has a guard. It refuses to modify a database unless it is
   local, has a single-label host (a Docker/CI service like `postgres`) or
   has a test marker in its name (`app_test`, `e2e`, `ci`, `sandbox`).
   **Never set `STILLRIGHT_ALLOW_DATABASE` yourself.** If the guard blocks
   a run, show the user its message and let them decide.
3. Make sure the app under test points at that same disposable database,
   and that its outbound calls go to the stillright proxy, not to real
   providers (see "Effects").

## Setup

```sh
command -v stillright || go install github.com/stillright-dev/stillright/cmd/stillright@latest
stillright version
```

The app must be running locally, connected to the test database. Start it
in the background and wait for it to answer before running scenarios.

## Workflow

### 1. Understand the handlers

Read the webhook/event handlers and the schema: migrations, `schema.sql`,
ORM models, or Mongoose schemas and `createIndex` calls for MongoDB. For each handler, note:

- what the gateway sends (event types, the ids it carries: event id,
  payment id, customer id);
- what it writes, and which rows must be unique;
- what it calls outside: e-mail, payment or partner APIs;
- what order the business assumes, for example "refund after payment".

Look for the classic bugs:

- check-then-insert without a unique constraint;
- no idempotency on the event id;
- handlers that assume an earlier event already arrived;
- side effects sent before commit, or sent without a claim.

### 2. Capture real payloads

Put real example payloads in `stillright/fixtures/*.json`. Take them from the
gateway's documentation or the user's logs, and use test data only. Keep the
same event id across deliveries: that is what a real redelivery looks like.

### 3. Write scenarios

Write one scenario per business flow, in `stillright/<flow>.yaml`:

```yaml
version: 1
name: payment-confirmed
target:
  base_url: http://localhost:3000
  database_url: ${DATABASE_URL}
setup:
  sql: fixtures/seed.sql            # rows the events reference (the customer...)
  reset: truncate
  reset_tables: [customers, subscriptions, payments]   # every table the flow writes
events:
  - id: paid
    request:
      method: POST
      path: /webhooks/payment
      headers: { X-Webhook-Token: "${WEBHOOK_TOKEN}" }   # if the app checks a token
      body_file: fixtures/payment_confirmed.json
  - id: refunded
    request: { method: POST, path: /webhooks/payment, body_file: fixtures/payment_refunded.json }
    after: paid                     # the business order; reorder may break it
perturb:
  duplicate: 3
  concurrency: 3
  reorder: true                     # only when order is not guaranteed (it almost never is)
  delay_ms: [0, 30]
runs: 100
invariants:
  - name: one active subscription per customer
    sql: |
      SELECT customer_id, count(*) AS active FROM subscriptions
      WHERE status = 'active' GROUP BY 1 HAVING count(*) > 1
```

#### MongoDB

When `database_url` is `mongodb://…/db` or `mongodb+srv://…/db`, three
parts of the scenario change: the setup, the reset and the invariants.

```yaml
setup:
  seed: fixtures/seed.json          # {"clientes": [{"_id": 42, "nome": "Ana"}]}, Extended JSON
  reset: truncate                   # deleteMany({}); indexes are kept
  reset_collections: [clientes, assinaturas, pagamentos]
invariants:
  - name: one active subscription per customer
    collection: assinaturas
    pipeline:
      - $match: { status: ativa }
      - $group: { _id: $cliente_id, ativas: { $sum: 1 }, ids: { $push: $_id } }
      - $match: { ativas: { $gt: 1 } }
  - name: refunded payment leaves no active subscription
    collection: assinaturas
    filter: { status: ativa, estornada: true }
```

Rules for MongoDB invariants:

- Use a pipeline (`$group` + `$match`) for "more than one" rules and
  `$lookup` across collections. Use a filter for "this must not exist".
- `$out`, `$merge`, `$where`, `$function` and `$accumulator` are refused.
- Extended JSON works inside queries, for example `{ $oid: "…" }`.
- Missing unique indexes are a classic MongoDB bug. Check what the app
  creates with `createIndex` / `unique: true`.

Full details are in `docs/mongodb.md` in the stillright repository.

#### Writing good invariants

Each invariant selects the bad state: "what must never be true?". Good ones
for payment-like flows:

- one row per external id (payment, order or event id);
- at most one active subscription, order or reservation per owner;
- money refunded means no access granted or goods shipped;
- balances and stock never negative;
- totals equal the sum of their items.

Return the columns that help debug, because they are printed as evidence.
**Never write an invariant that is just the current behavior.** It must be a
business rule the user would agree with. Show the user the invariants you
picked.

If the app sends a 5xx for a duplicate it correctly rejected, that counts as
`ERROR` by default. Add `accept_status: [200, 409]` (or `500`) to that event
only if the user agrees that answer means "handled".

### 4. Effects (outbound calls)

If the handler sends e-mails or calls APIs, stub them through the proxy.
Otherwise the app's calls fail and every run is an `ERROR`:

```yaml
proxy:
  listen: 127.0.0.1:8899
  stubs:
    - { host: api.email.local, status: 202 }
effects:
  - name: exactly one welcome e-mail
    assert: count(host == "api.email.local" && path == "/send" && body contains "welcome") == 1
```

Route the app through the proxy in one of these ways:

- point its provider base URL at `http://127.0.0.1:8899/<provider-host>`
  (works with any client);
- `HTTP_PROXY=http://127.0.0.1:8899` for Python, Go, Ruby and curl;
- for Node's `fetch`, `NODE_USE_ENV_PROXY=1` plus `HTTP_PROXY`.

HTTPS is not intercepted. Prefer `== 1` over `<= 1` when the call must
happen, because `<= 1` passes vacuously when nothing reaches the proxy.

### 5. Run and read the report

```sh
stillright run stillright/flow.yaml --keep-going --report-json /tmp/sr.json
```

The exit code says what happened:

- `0`: everything passed;
- `1`: a rule broke;
- `2`: something could not be verified.

Read `/tmp/sr.json` rather than parsing the terminal output:

| Field | Use |
| --- | --- |
| `status`, `error` | top-level outcome; `error` explains an exit code 2 before any run |
| `checks[].status` | per invariant or effect: `PASS`, `FAIL` or `ERROR` |
| `checks[].shrunk.sequence.text` | the smallest failing sequence; explain the bug from it |
| `checks[].shrunk.reproduced` / `attempts` | below `attempts` means timing-dependent |
| `checks[].shrunk.evidence` or `first_failure.evidence` | the offending rows or calls |
| `checks[].reproduce` | commands to rerun it |
| `stopped_at.deliveries[].error` | why a run could not be verified (no response, 5xx) |
| `warnings` | results that are probably vacuous; treat them as not verified |

How to read the smallest sequence:

- `[x] → [x]`: plain redelivery breaks it, so there is no idempotency.
- `[x ‖ x]`: concurrent deliveries break it, so it is a race. Look for
  check-then-act.
- `[b] → [a]`, where `b` is declared `after: a`: an ordering bug. The
  handler assumes `a` came first.

**`ERROR` is never a pass.** Fix the cause before going further: the app is
not running, the token is missing, a 5xx, bad SQL, a missing proxy stub or
the database guard.

### 6. Fix and verify

1. Explain the bug to the user with the smallest sequence and the evidence.
2. Fix the app, not the scenario. Prefer database guarantees plus
   idempotent handlers:
   - unique constraints or partial unique indexes (MongoDB: a unique index,
     with `partialFilterExpression` for "one active per customer");
   - `INSERT ... ON CONFLICT` on the event or external id (MongoDB:
     `updateOne(..., {$setOnInsert}, {upsert: true})` on the external id, or
     an insert that treats the duplicate-key error 11000 as "already
     processed");
   - a row lock (`SELECT ... FOR UPDATE`) to serialize events for one owner
     (MongoDB: a transaction, or a conditional `findOneAndUpdate` acting as
     the claim);
   - handlers that tolerate any order;
   - side effects claimed inside the transaction and sent after commit (or
     an outbox).
3. Confirm with the exact failing sequence, retried because races are
   timing-dependent:
   ```sh
   stillright run stillright/flow.yaml --sequence '[paid ‖ paid]' --attempts 20
   ```
4. Then explore again, with a couple of different seeds:
   ```sh
   stillright run stillright/flow.yaml --keep-going --runs 200
   ```

Do not weaken, delete or skip an invariant to make a run pass. Do not raise
`accept_status`, remove `reorder` or lower `duplicate` without the user's
agreement.

### 7. Keep it in CI

Add the GitHub Action so the scenarios run on every pull request. The
workflow must start PostgreSQL (a service container) and the app first:

```yaml
- uses: stillright-dev/stillright@v0.1.0
  with:
    scenarios: stillright
    args: --keep-going
```

A full example is in the stillright repository at `docs/ci.md`.

## Command reference

```
stillright run <scenario.yaml> [--seed N] [--runs N] [--keep-going]
               [--replay N | --sequence '[a ‖ a] → [b]'] [--attempts N] [--no-shrink]
               [--http-timeout 10s] [--report-json F] [--report-html F] [--summary F]
```

`--replay N` needs the `--seed` that generated sequence N. `--sequence`
accepts `|` and `->` as well. `settle_ms` in the scenario waits for
background work before checking.

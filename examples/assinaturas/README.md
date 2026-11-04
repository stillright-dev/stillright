# Example: a subscription webhook with real bugs

A payment gateway calls `POST /webhooks/pagamento` when a payment is confirmed
(`pagamento.confirmado`) or refunded (`pagamento.estornado`). The handler in
[`server.js`](server.js) is the kind of code that passes every test and every
code review. It works whenever each webhook arrives once, in order and one at
a time. Gateways do not promise any of that.

It has three common bugs:

| | Bug | Why it happens |
| --- | --- | --- |
| 1 | duplicate payment rows and a second active subscription | "check, then insert" with no unique constraint and no idempotency key |
| 2 | a refunded payment leaves an active subscription | the refund handler assumes the payment already exists; when the refund arrives first it updates nothing and answers 200 |
| 3 | the welcome e-mail goes out twice | the same check-then-act, on an outbound call |

[`fixed/`](fixed) has the same endpoint and payloads with the bugs fixed:

- unique constraints, and the event id used as the idempotency key (`INSERT ... ON CONFLICT DO NOTHING`);
- a row lock that processes one customer's events one at a time;
- a refund-aware payment handler;
- the e-mail claimed inside the transaction and sent after commit.

The three scenarios in [`stillright/`](stillright) find each bug.

## Run it

You need Node 20+, PostgreSQL and `stillright` (see the main README). If you
don't have PostgreSQL, `docker compose up -d` starts one on port 5433.

```sh
createdb assinaturas
export DATABASE_URL=postgres://localhost/assinaturas
npm install

# Buggy version. E-mails go to the stillright proxy, never to a real provider.
npm run db:setup
EMAIL_API_URL=http://127.0.0.1:8899/api.email.local npm start &

stillright run stillright/assinatura-duplicada.yaml --keep-going
stillright run stillright/estorno-fora-de-ordem.yaml
stillright run stillright/email-boas-vindas.yaml
```

## Before

The same seed explores the same sequences, but whether a race fires depends
on timing. Your counts and sequence numbers may differ. The smallest
sequences should not.

```
$ stillright run stillright/assinatura-duplicada.yaml --seed 8812 --keep-going
FAIL   uma assinatura ativa por cliente
       failed in 2 of 50 runs; first in sequence 26: [pago+15ms ‖ pago+16ms]
       smallest sequence: [pago ‖ pago]   (2 simultaneous deliveries; reproduced 5/5)
       evidence: cliente_id=42 ativas=2
       reproduce: stillright run stillright/assinatura-duplicada.yaml --seed 8812 --replay 26
                  stillright run stillright/assinatura-duplicada.yaml --sequence '[pago ‖ pago]'
FAIL   um registro por pagamento do gateway
       failed in 31 of 50 runs; first in sequence 1: [pago+16ms] → [pago+6ms]
       smallest sequence: [pago] → [pago]   (2 deliveries, one at a time; reproduced 5/5)
       evidence: gateway_id=pay_123 registros=2

$ stillright run stillright/estorno-fora-de-ordem.yaml --seed 8812
FAIL   pagamento estornado não deixa assinatura ativa
       failed in 1 of 2 runs; first in sequence 1: [estornado] → [estornado+21ms] → [pago] → [pago+22ms]
       smallest sequence: [estornado] → [pago]   (2 deliveries, one at a time; reproduced 5/5)
       evidence: assinatura_id=1 cliente_id=42

$ stillright run stillright/email-boas-vindas.yaml --seed 8812
FAIL   exatamente um e-mail de boas-vindas
       failed in 1 of 4 runs; first in sequence 3: [pago+2ms ‖ pago]
       smallest sequence: [pago ‖ pago]   (2 simultaneous deliveries; reproduced 5/5)
       2 matching calls, expected == 1
       evidence: 2 rows
       method | host            | path  | body
       -------+-----------------+-------+-------------------------------------------------------------
       POST   | api.email.local | /send | {"para":"ana@example.com","template":"boas-vindas","dados":…
       POST   | api.email.local | /send | {"para":"ana@example.com","template":"boas-vindas","dados":…
```

The smallest sequences read like bug reports:

- `[pago] → [pago]`: a plain redelivery, one after the other, records the
  payment twice. There is no idempotency at all.
- `[pago ‖ pago]`: two simultaneous deliveries create two active
  subscriptions and send two e-mails. This is a race.
- `[estornado] → [pago]`: a refund that arrives first is lost. This is an
  ordering bug, not a race, so it fails every time.

## After

```sh
kill %1
npm run db:setup:fixed
EMAIL_API_URL=http://127.0.0.1:8899/api.email.local npm run start:fixed &
```

```
$ stillright run stillright/assinatura-duplicada.yaml --keep-going --runs 100
PASS   uma assinatura ativa por cliente
PASS   um registro por pagamento do gateway
100 of 100 runs: 100 passed, 0 failed, 0 could not be verified

$ stillright run stillright/estorno-fora-de-ordem.yaml --keep-going --runs 100
PASS   pagamento estornado não deixa assinatura ativa
PASS   pagamento com estorno recebido fica estornado
100 of 100 runs: 100 passed, 0 failed, 0 could not be verified

$ stillright run stillright/email-boas-vindas.yaml --keep-going --runs 100
PASS   exatamente um e-mail de boas-vindas
100 of 100 runs: 100 passed, 0 failed, 0 could not be verified
```

## What changed

Compare [`db/schema.sql`](db/schema.sql) with
[`fixed/schema.sql`](fixed/schema.sql), and [`server.js`](server.js) with
[`fixed/server.js`](fixed/server.js). The fix has two layers:

- **The database makes the bad states impossible.**
  - A partial unique index allows one *active* subscription per customer.
  - `gateway_id` and `evento_id` are unique.
  - `(cliente_id, tipo)` is unique for sent e-mails.
- **The handler stops relying on luck.**
  - A redelivered event is a no-op (`ON CONFLICT (evento_id) DO NOTHING`).
  - A customer's events are processed one at a time (`SELECT ... FOR UPDATE`).
  - A payment checks whether its refund already arrived.
  - The e-mail is sent only by the delivery that wins the claim, after commit.

The constraints alone would turn the bugs into 500 errors, which the gateway
retries. The handler changes make duplicates and reorderings succeed
quietly. stillright checks the end state either way. To let a 500 count as
handled, add `accept_status: [200, 500]` to the event.

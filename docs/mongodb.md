# MongoDB

Stillright works the same with MongoDB as with PostgreSQL: it delivers your
events duplicated, concurrently and out of order, then checks invariants
that must return nothing. Only the database parts of the scenario change.

A scenario is a MongoDB one when `target.database_url` starts with
`mongodb://` or `mongodb+srv://`. The URI must name the database:

```yaml
target:
  base_url: http://localhost:3000
  database_url: ${MONGODB_URI}       # e.g. mongodb://localhost:27017/assinaturas_test
```

## Setup

```yaml
setup:
  seed: fixtures/seed.json           # optional; inserted before every sequence
  reset: truncate                    # truncate | none
  reset_collections: [clientes, assinaturas, pagamentos]
```

- **Reset** deletes every document in the listed collections
  (`deleteMany({})`). It **keeps the collections and their indexes**, because
  unique indexes are often what makes the app correct.
- **The seed** is a JSON object that maps collection names to arrays of
  documents. Collections are inserted in file order. It is
  [Extended JSON](https://www.mongodb.com/docs/manual/reference/mongodb-extended-json/),
  so ids and dates keep their types:

  ```json
  {
    "clientes": [
      { "_id": 42, "nome": "Ana", "plano": { "$oid": "65f1a2b3c4d5e6f708091a2b" } }
    ],
    "planos": [
      { "_id": { "$oid": "65f1a2b3c4d5e6f708091a2b" }, "criado_em": { "$date": "2026-01-01T00:00:00Z" } }
    ]
  }
  ```

## Invariants

Each invariant names a `collection` and either an aggregation `pipeline` or
a find `filter`, written in YAML. Like SQL invariants, they must return
**zero documents**. Any document returned is the evidence.

```yaml
invariants:
  - name: uma assinatura ativa por cliente
    collection: assinaturas
    pipeline:
      - $match: { status: ativa }
      - $group: { _id: $cliente_id, ativas: { $sum: 1 }, ids: { $push: $_id } }
      - $match: { ativas: { $gt: 1 } }

  - name: um pagamento por id do gateway
    collection: pagamentos
    pipeline:
      - $group: { _id: $gateway_id, n: { $sum: 1 } }
      - $match: { n: { $gt: 1 } }

  - name: estorno não deixa assinatura ativa
    collection: assinaturas
    filter: { status: ativa, estornada: true }
```

Things to know:

- **Key order is kept**, so `$sort` and `$group` keys behave as written.
- **Extended JSON values work** inside queries, for example
  `{ _id: { $oid: "…" } }` or `{ criada_em: { $lt: { $date: "2026-01-01T00:00:00Z" } } }`.
- **Prefer a pipeline** for "more than one" rules (`$group` + `$match`) and
  for rules across collections (`$lookup`). Use a filter for "this must not
  exist".
- **Evidence** shows the top-level fields of the returned documents (up to
  10), with values in relaxed Extended JSON. A field a document lacks is
  shown as `NULL`. Add `$project` to keep the evidence readable.

### What is not allowed

An invariant must not change the data it inspects or run code, so
stillright refuses these operators when it loads the scenario:

| Operator | Why |
| --- | --- |
| `$out`, `$merge` | they write to the database |
| `$where`, `$function`, `$accumulator` | they run JavaScript inside the database |

MongoDB has no read-only transaction to fall back on, as PostgreSQL does.
Anything else your server supports is passed through as written. A typo
such as `$matchh` comes back from the server as an `ERROR`, never a pass.

## The database guard

The [database guard](../README.md#install) works the same way. With
`reset: truncate` or a seed, stillright runs only against:

- local hosts;
- single-label hosts, such as a CI service named `mongo`;
- databases with a test marker in their name.

If one host of a replica set is remote, the database counts as remote. An
Atlas cluster is identified by its `mongodb+srv` host:

```sh
STILLRIGHT_ALLOW_DATABASE=cluster0.abcd.mongodb.net/assinaturas stillright run ...
```

## In CI

Add MongoDB as a service container next to your app:

```yaml
services:
  mongo:
    image: mongo:8
    ports: ["27017:27017"]
env:
  MONGODB_URI: mongodb://localhost:27017/assinaturas_test
```

## Limitations

- One database per scenario. A scenario cannot reset collections in two
  databases.
- Invariants run with the default read concern of your deployment. On a
  replica set, use a URI that reads from the primary, which is the default,
  so you see the app's latest writes.
- Change streams, transactions in the app, and sharded clusters are not
  specially handled. Stillright only sees the end state.

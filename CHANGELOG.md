# Changelog

## Unreleased

- **MongoDB.** A `mongodb://` or `mongodb+srv://` `database_url` makes a
  scenario a MongoDB one:
  - invariants are an aggregation `pipeline` or a find `filter` on a
    `collection`, and must return no documents;
  - setup uses a `seed` JSON file and `reset_collections`, and the reset
    keeps indexes;
  - `$out`, `$merge`, `$where`, `$function` and `$accumulator` are refused.

  See `docs/mongodb.md`.

- **Database guard.** Scenarios that truncate tables or run setup SQL are
  refused when the database does not look disposable: not local, not a
  single-label service host, and no test marker in its name. To allow one
  database explicitly, set `STILLRIGHT_ALLOW_DATABASE=host/database`.
- **Claude Code skill** in `skills/stillright`. It teaches Claude to write
  scenarios for an app, run them, read `report.json` and fix what breaks.

## v0.1.0

The first release. Stillright checks that an application's business rules,
written as SQL invariants, still hold when its webhooks are duplicated,
delivered concurrently or out of order.

- **Scenarios** in YAML. Every error is reported with its line and field.
  `${VAR}` values come from the environment.
- **Runner.** It resets the database, runs the seed SQL and delivers events
  over HTTP. Invariants run in a read-only transaction. Each check ends as
  `PASS`, `FAIL` or `ERROR`, and the exit code is 0, 1 or 2. Anything that
  cannot be verified is an `ERROR`, never a `PASS`.
- **Deterministic perturbation.** Sequences are generated from `--seed`.
  Events are duplicated, sent in concurrent batches released behind a
  barrier, reordered (violating `after`) and delayed. Sequence 0 is always
  the declared order. `accept_status` sets which responses count as handled
  for each event.
- **Shrinking.** A failure is reduced to the smallest sequence that still
  fails, preferring sequential deliveries over concurrent ones. Each step is
  confirmed over `--attempts` runs, and the reproduction rate is reported.
  `--replay N` reruns a generated sequence and `--sequence` runs a
  hand-written one.
- **Effects.** A local proxy records outbound calls and answers them with
  stubs. It accepts `HTTP_PROXY`, CONNECT tunnels carrying plain HTTP, and
  direct URLs. Assertions are written as `count(...)` expressions.
  `settle_ms` waits for background work. You are warned when effect results
  are probably vacuous.
- **Reports:**
  - `--report-json`, with a versioned schema (`stillright.report/v1`);
  - `--report-html`, a self-contained page that works on a phone and in
    dark mode;
  - `--summary`, Markdown for CI job summaries.
- **GitHub Action.** It runs every scenario in a folder, publishes the job
  summary and uploads the reports.
- **Example.** `examples/assinaturas` is a Node webhook with three planted
  bugs, the scenarios that find them, and the fix.

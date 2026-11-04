# Running stillright in CI

The repository ships a GitHub Action. It does the following:

1. builds `stillright` from the action's own source, using its `go.mod`;
2. runs every `*.yaml` scenario in a folder, even after one fails;
3. writes a JSON and an HTML report per scenario;
4. adds a summary to the job page;
5. uploads the reports as an artifact;
6. fails the job when any scenario fails or cannot be verified.

## Example

The application needs to be running, with its database, before stillright
starts. Here PostgreSQL runs as a service container and the app is started
in the background:

```yaml
name: stillright
on: [pull_request]

jobs:
  invariants:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:17
        env:
          POSTGRES_PASSWORD: postgres
          POSTGRES_DB: app_test
        ports: ["5432:5432"]
        options: >-
          --health-cmd pg_isready --health-interval 5s --health-timeout 5s --health-retries 10
    env:
      DATABASE_URL: postgres://postgres:postgres@localhost:5432/app_test?sslmode=disable
    steps:
      - uses: actions/checkout@v4

      - name: Start the app
        run: |
          npm ci
          npm run migrate
          # Route outbound calls through the effects proxy (see docs/effects.md).
          NODE_USE_ENV_PROXY=1 HTTP_PROXY=http://127.0.0.1:8899 npm start &
          npx wait-on http://localhost:3000/health

      - uses: stillright-dev/stillright@v0.1.0
        with:
          scenarios: stillright          # folder with your *.yaml scenarios
          args: --runs 100
```

## Inputs

| Input | Default | Meaning |
| --- | --- | --- |
| `scenarios` | `stillright` | folder with `*.yaml` / `*.yml` scenarios, or one file |
| `args` | | extra flags for every run, e.g. `--runs 50 --seed 8812 --keep-going` |
| `reports-dir` | `stillright-reports` | where `<scenario>.json` and `<scenario>.html` go |
| `upload-reports` | `true` | upload the reports folder as an artifact |
| `artifact-name` | `stillright-reports` | the artifact's name |

The `exit-code` output is `0` (all passed), `1` (something failed) or `2`
(something could not be verified).

## Seeds in CI

Without `--seed`, every CI run explores different sequences. That is
deliberate: over many pull requests you cover far more orderings than one
fixed seed would. The seed is always printed, in the logs, the summary and
`report.json`, so any failure can be reproduced locally:

```sh
stillright run stillright/pagamento.yaml --seed 8812 --replay 6
```

If you need the exact same sequences on every run, for example while fixing
a flaky failure, pin the seed with `args: --seed 8812`.

## Other CI systems

There is no action outside GitHub. Build or download the binary and run it.
The reports and exit codes work the same way:

```sh
stillright run stillright/pagamento.yaml \
  --report-json reports/pagamento.json \
  --report-html reports/pagamento.html \
  --summary reports/summary.md
```

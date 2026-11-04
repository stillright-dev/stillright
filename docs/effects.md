# Effects: checking outbound calls

Invariants check what ended up in the database. Some bugs only show up in
what the application *sends*: two welcome e-mails, a refund requested twice,
a webhook forwarded to a partner three times. Effects check those.

When a scenario has `effects` (or a `proxy` section), stillright starts a
local HTTP proxy for the whole run. The application sends its outbound calls
to it; the proxy **records them and answers with stubs**. Nothing is forwarded
to the real provider and nothing leaves your machine.

```yaml
proxy:
  listen: 127.0.0.1:8899        # default
  stubs:                        # optional; unknown hosts get 200 {}
    - host: api.email.local
      status: 202
      body: '{"queued": true}'
      headers: { X-Request-Id: test }
settle_ms: 200                  # optional; wait for background work before checking
effects:
  - name: at most one welcome e-mail
    assert: count(host == "api.email.local" && path == "/send" && body contains "welcome") <= 1
```

The calls are cleared before each run, right after the database reset.

## Routing the application through the proxy

You have three options. Pick whichever your HTTP client supports.

**1. `HTTP_PROXY`.** Most clients in Python (`requests`, `httpx`), Go
(`net/http`), Ruby and curl honor it:

```sh
HTTP_PROXY=http://127.0.0.1:8899 npm start
```

**Node's built-in `fetch` ignores `HTTP_PROXY`** unless you also set
`NODE_USE_ENV_PROXY=1` (Node 24+). Axios and `node-fetch` need their own
proxy settings, or option 3.

```sh
NODE_USE_ENV_PROXY=1 HTTP_PROXY=http://127.0.0.1:8899 node server.js
```

Clients that tunnel through `CONNECT` even for `http://` URLs, like Node's
`fetch`, are supported: the proxy serves plain HTTP inside the tunnel.

**2. `HTTPS_PROXY`.** HTTPS calls reach the proxy as `CONNECT` tunnels. There
is no TLS interception in v0.1, so the proxy records the **host only**
(method `CONNECT`, no path or body) and then closes the tunnel, and the
app's call fails. To check paths and bodies, point the app at an `http://`
URL for that provider in your test environment (option 3 does this too).

**3. Direct.** Point the provider's base URL at the proxy and put the real
host in the first path segment:

```sh
EMAIL_API_URL=http://127.0.0.1:8899/api.email.local npm start
# POST http://127.0.0.1:8899/api.email.local/send
#   is recorded as host api.email.local, path /send
```

This works with any client, because no proxy support is needed.

## Assertions

An assertion counts the recorded calls that match a condition and compares
the count with a number:

```
count(<condition>) <op> <number>
```

| Part | Allowed |
| --- | --- |
| fields | `method`, `host`, `path`, `query`, `body` |
| field tests | `==`, `!=`, `contains` (with a quoted string) |
| combining | `&&`, `\|\|`, `!`, parentheses |
| comparing the count | `==`, `!=`, `<=`, `>=`, `<`, `>` |

`count()` with no condition counts every call. `host` has no port and is
lower-case. `method` is compared without regard to case. Assertions are
parsed and interpreted, never evaluated as code.

```
count(host == "api.email.local" && path == "/send") <= 1
count(host == "payments.local" && path == "/refunds") == 1
count(method == "POST" && body contains "\"order_id\":42") >= 1
count(!(host == "api.email.local")) == 0      # nothing else was called
```

When an assertion fails, the report shows the count and up to 10 matching
calls as evidence, and the failure is shrunk like any invariant.

## When effect results are meaningless

`count(...) <= 1` passes trivially if the app's calls never reach the proxy.
stillright warns you when:

- the proxy recorded **no calls at all** in any run, or
- an assertion filters on `host == "x"` and the proxy **never saw host x**.

Treat either warning as "not verified", not as a pass.

## Limitations in v0.1

- No HTTPS interception. `CONNECT` tunnels record the host only.
- One proxy per run, shared by all sequences. A call that arrives after its
  sequence ended (slow background jobs) is counted in the next one. Use
  `settle_ms` to wait for background work.
- Request bodies are recorded up to 64 KB.
- Stubs are static per host. They do not vary by path or request.

package effects

import (
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"time"
)

// Stub is the canned response for requests to one host.
type Stub struct {
	Host    string
	Status  int
	Body    string
	Headers map[string]string
}

// Proxy is a recording HTTP proxy. It never forwards anything: every request
// is recorded and answered from its stubs (200 "{}" by default).
//
// It accepts three ways in:
//   - proxy-style requests, from an app with HTTP_PROXY set;
//   - CONNECT tunnels carrying plain HTTP, which some clients (Node's fetch)
//     use even for http:// URLs; TLS tunnels are recorded by host and closed,
//     since there is no TLS interception;
//   - direct requests, from an app whose provider URL points at the proxy
//     with the provider's host as the first path segment:
//     http://127.0.0.1:8899/api.email.local/send is recorded as host
//     api.email.local, path /send.
type Proxy struct {
	ln    net.Listener
	srv   *http.Server
	stubs map[string]Stub

	mu    sync.Mutex
	calls []Call
	total int             // calls recorded since the proxy started, across resets
	hosts map[string]bool // every host seen since the proxy started
}

// Listen starts a proxy on addr, such as "127.0.0.1:8899".
func Listen(addr string, stubs []Stub) (*Proxy, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	p := &Proxy{ln: ln, stubs: map[string]Stub{}, hosts: map[string]bool{}}
	for _, s := range stubs {
		p.stubs[strings.ToLower(s.Host)] = s
	}
	p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	go p.srv.Serve(ln)
	return p, nil
}

// Addr is the address the proxy listens on.
func (p *Proxy) Addr() string { return p.ln.Addr().String() }

func (p *Proxy) Close() error { return p.srv.Close() }

func (p *Proxy) Reset() {
	p.mu.Lock()
	p.calls = nil
	p.mu.Unlock()
}

func (p *Proxy) Calls() []Call {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Call(nil), p.calls...)
}

// Total is how many calls were recorded since the proxy started.
func (p *Proxy) Total() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.total
}

// Hosts lists every host seen since the proxy started, sorted.
func (p *Proxy) Hosts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Sorted(maps.Keys(p.hosts))
}

func (p *Proxy) record(c Call) {
	p.mu.Lock()
	p.calls = append(p.calls, c)
	p.total++
	p.hosts[c.Host] = true
	p.mu.Unlock()
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.tunnel(w, r)
		return
	}
	p.serve(w, r, "")
}

// tunnel accepts a CONNECT. Plain HTTP inside the tunnel is served like any
// other request; a TLS handshake is recorded by host and the tunnel closed.
func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	host := hostname(r.Host)
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "stillright: cannot open tunnel", http.StatusInternalServerError)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	first, err := buf.Reader.Peek(1)
	if err != nil {
		return
	}
	if first[0] == 0x16 { // TLS handshake record
		p.record(Call{Method: http.MethodConnect, Host: host, At: time.Now()})
		return
	}
	for {
		req, err := http.ReadRequest(buf.Reader)
		if err != nil {
			return
		}
		rec := httptest.NewRecorder()
		p.serve(rec, req, host)
		resp := rec.Result()
		resp.ContentLength = int64(rec.Body.Len())
		if err := resp.Write(conn); err != nil || req.Close {
			return
		}
	}
}

// serve records one HTTP request and answers it from the stubs. tunnelHost
// is the CONNECT target when the request came through a tunnel.
func (p *Proxy) serve(w http.ResponseWriter, r *http.Request, tunnelHost string) {
	call := Call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, At: time.Now()}
	switch {
	case r.URL.Host != "": // proxy-style: absolute URL in the request line
		call.Host = hostname(r.URL.Host)
	case tunnelHost != "":
		call.Host = tunnelHost
	default: // direct: /<host>/<path>
		seg, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		call.Host, call.Path = hostname(seg), "/"+rest
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, MaxBodyBytes+1))
	if len(body) > MaxBodyBytes {
		body, call.Truncated = body[:MaxBodyBytes], true
	}
	io.Copy(io.Discard, r.Body)
	call.Body = body
	p.record(call)

	stub, ok := p.stubs[call.Host]
	if !ok {
		stub = Stub{Status: http.StatusOK, Body: "{}"}
	}
	for k, v := range stub.Headers {
		w.Header().Set(k, v)
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	status := stub.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	io.WriteString(w, stub.Body)
}

func hostname(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.ToLower(h)
	}
	return strings.ToLower(strings.Trim(hostport, "[]"))
}

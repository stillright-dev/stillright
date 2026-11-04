package effects

import (
	"io"
	"net"
	"net/http"
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
// It accepts both proxy-style requests (the application sets HTTP_PROXY) and
// direct ones (the application's provider URL points at the proxy). HTTPS
// tunnels (CONNECT) are recorded by host and refused, since there is no TLS
// interception.
type Proxy struct {
	ln    net.Listener
	srv   *http.Server
	stubs map[string]Stub

	mu    sync.Mutex
	calls []Call
	total int // calls recorded since the proxy started, across resets
}

// Listen starts a proxy on addr, such as "127.0.0.1:8899".
func Listen(addr string, stubs []Stub) (*Proxy, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	p := &Proxy{ln: ln, stubs: map[string]Stub{}}
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

func (p *Proxy) record(c Call) {
	p.mu.Lock()
	p.calls = append(p.calls, c)
	p.total++
	p.mu.Unlock()
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	call := Call{Method: r.Method, At: time.Now()}

	if r.Method == http.MethodConnect {
		call.Host = hostname(r.Host)
		p.record(call)
		http.Error(w, "stillright: HTTPS is recorded by host but not intercepted; point the app at an http:// URL for this provider", http.StatusBadGateway)
		return
	}

	host := r.URL.Host // proxy-style: absolute URL in the request line
	if host == "" {
		host = r.Host // direct: the app called the proxy itself
	}
	call.Host = hostname(host)
	call.Path = r.URL.Path
	call.Query = r.URL.RawQuery
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

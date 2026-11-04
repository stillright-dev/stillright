package effects

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

var calls = []Call{
	{Method: "POST", Host: "api.email.local", Path: "/send", Body: []byte(`{"template":"welcome","to":"ana@x"}`)},
	{Method: "POST", Host: "api.email.local", Path: "/send", Body: []byte(`{"template":"receipt"}`)},
	{Method: "GET", Host: "api.email.local", Path: "/status", Query: "id=1"},
	{Method: "POST", Host: "payments.local", Path: "/refunds"},
}

func TestAssertions(t *testing.T) {
	cases := []struct {
		src     string
		matches int
		holds   bool
	}{
		{`count(host == "api.email.local" && path == "/send") <= 1`, 2, false},
		{`count(host == "api.email.local" && path == "/send" && body contains "welcome") <= 1`, 1, true},
		{`count() == 4`, 4, true},
		{`count(method == "post") >= 3`, 3, true},
		{`count(host == "payments.local" || query == "id=1") == 2`, 2, true},
		{`count(!(host == "api.email.local")) < 1`, 1, false},
		{`count(host != "api.email.local") > 0`, 1, true},
		{`count((path == "/send" || path == "/status") && method != "GET") != 2`, 2, false},
		{`count(body contains "\"template\"") == 2`, 2, true},
	}
	for _, tc := range cases {
		a, err := ParseAssertion(tc.src)
		if err != nil {
			t.Fatalf("%s: %v", tc.src, err)
		}
		m := a.Matching(calls)
		if len(m) != tc.matches || a.Holds(len(m)) != tc.holds {
			t.Errorf("%s: %d matches (want %d), holds=%v (want %v)", tc.src, len(m), tc.matches, a.Holds(len(m)), tc.holds)
		}
	}
}

func TestAssertionErrors(t *testing.T) {
	for src, want := range map[string]string{
		`size(host == "x") <= 1`:         "column 1: an assertion starts with count(...)",
		`count(host == "x") <= `:         "column 23: expected a whole number to compare the count with (reached the end)",
		`count(host = "x") <= 1`:         `column 12: unexpected character '='`,
		`count(hots == "x") <= 1`:        `column 7: unknown field "hots"`,
		`count(host == x) <= 1`:          "column 15: expected a quoted string",
		`count(host == "x) <= 1`:         "column 15: unterminated string",
		`count(host == "x") 1`:           "column 20: expected a comparison",
		`count(host == "x") <= 1 && 2`:   `column 25: unexpected "&&" after the assertion`,
		`count(host == "x" && ) <= 1`:    "column 22: expected a condition",
		`count(host contain "x") <= 1`:   "column 12: expected ==, != or contains after host",
		`count((host == "x") <= 1`:       `column 21: expected ")"`,
		`os.exit(1)`:                     "column 3: unexpected character '.'",
		`exit(1)`:                        "column 1: an assertion starts with count(...)",
		`count(host == "x"); rm -rf / 1`: "column 19: unexpected character ';'",
	} {
		_, err := ParseAssertion(src)
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s\n got  %v\n want %s", src, err, want)
		}
	}
}

func startProxy(t *testing.T, stubs ...Stub) *Proxy {
	p, err := Listen("127.0.0.1:0", stubs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestProxyRecordsAndStubs(t *testing.T) {
	p := startProxy(t, Stub{Host: "api.email.local", Status: 202, Body: `{"queued":true}`, Headers: map[string]string{"X-Id": "7"}})
	proxyURL, _ := url.Parse("http://" + p.Addr())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	resp, err := client.Post("http://api.email.local/send?x=1", "application/json", strings.NewReader(`{"to":"ana"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 202 || string(body) != `{"queued":true}` || resp.Header.Get("X-Id") != "7" {
		t.Errorf("stub response = %d %s %v", resp.StatusCode, body, resp.Header)
	}

	// Unknown hosts get 200 {} and nothing leaves the machine.
	resp, err = client.Get("http://example.invalid:8080/anything")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "{}" || resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("default response = %d %s", resp.StatusCode, body)
	}

	// Direct calls name the host in the first path segment.
	resp, err = http.Post("http://"+p.Addr()+"/API.Email.local/send?y=2", "text/plain", strings.NewReader("hi"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	got := p.Calls()
	if len(got) != 3 {
		t.Fatalf("calls = %+v", got)
	}
	if c := got[0]; c.Method != "POST" || c.Host != "api.email.local" || c.Path != "/send" || c.Query != "x=1" || string(c.Body) != `{"to":"ana"}` {
		t.Errorf("call 0 = %+v", c)
	}
	if c := got[1]; c.Host != "example.invalid" || c.Path != "/anything" {
		t.Errorf("call 1 = %+v", c)
	}
	if c := got[2]; c.Host != "api.email.local" || c.Path != "/send" || c.Query != "y=2" || string(c.Body) != "hi" {
		t.Errorf("call 2 = %+v", c)
	}

	if h := strings.Join(p.Hosts(), ","); h != "api.email.local,example.invalid" {
		t.Errorf("hosts = %s", h)
	}
	p.Reset()
	if len(p.Calls()) != 0 || p.Total() != 3 {
		t.Errorf("after reset: %d calls, total %d", len(p.Calls()), p.Total())
	}
}

func TestProxyConnectIsRecordedAndRefused(t *testing.T) {
	p := startProxy(t)
	proxyURL, _ := url.Parse("http://" + p.Addr())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	if _, err := client.Get("https://api.stripe.example/v1/charges"); err == nil {
		t.Fatal("HTTPS through the proxy should fail without interception")
	}
	got := p.Calls()
	if len(got) != 1 || got[0].Method != "CONNECT" || got[0].Host != "api.stripe.example" {
		t.Errorf("calls = %+v", got)
	}
}

func TestProxyTruncatesBody(t *testing.T) {
	p := startProxy(t)
	big := bytes.Repeat([]byte("x"), MaxBodyBytes+100)
	resp, err := http.Post("http://"+p.Addr()+"/up", "text/plain", bytes.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	c := p.Calls()[0]
	if len(c.Body) != MaxBodyBytes || !c.Truncated {
		t.Errorf("body %d bytes, truncated=%v", len(c.Body), c.Truncated)
	}
}

// Node's fetch tunnels even plain http:// requests through CONNECT.
func TestProxyServesPlainHTTPInsideTunnel(t *testing.T) {
	p := startProxy(t, Stub{Host: "api.email.local", Status: 202, Body: `{"ok":1}`})
	conn, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	io.WriteString(conn, "CONNECT api.email.local:80 HTTP/1.1\r\nHost: api.email.local:80\r\n\r\n")
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("CONNECT: %v %v", resp, err)
	}
	for i := 0; i < 2; i++ { // keep-alive: two requests on one tunnel
		io.WriteString(conn, "POST /send HTTP/1.1\r\nHost: api.email.local\r\nContent-Length: 5\r\n\r\nhello")
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 202 || string(body) != `{"ok":1}` {
			t.Errorf("tunneled response = %d %s", resp.StatusCode, body)
		}
	}
	got := p.Calls()
	if len(got) != 2 || got[0].Host != "api.email.local" || got[0].Path != "/send" || string(got[0].Body) != "hello" {
		t.Errorf("calls = %+v", got)
	}
}

func TestAssertionHosts(t *testing.T) {
	a, _ := ParseAssertion(`count((host == "A.local" && path == "/x") || !(host == "b.local") || host != "c.local") >= 1`)
	if got := strings.Join(a.Hosts(), ","); got != "a.local,b.local" {
		t.Errorf("hosts = %s", got)
	}
}

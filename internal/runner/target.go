package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stillright/stillright/internal/scenario"
)

// Target is the application under test.
type Target interface {
	Send(ctx context.Context, req scenario.Request) (Response, error)
}

type Response struct {
	Status   int
	Duration time.Duration
	Body     []byte // first bytes of the response body, for error messages
}

// maxResponseSnippet bounds how much of a response body is kept.
const maxResponseSnippet = 512

// UserAgent is sent with every delivery so the application can tell
// stillright traffic apart in its logs.
var UserAgent = "stillright"

// HTTPTarget delivers events to an application over HTTP.
type HTTPTarget struct {
	BaseURL string
	Client  *http.Client
}

func (t *HTTPTarget) Send(ctx context.Context, r scenario.Request) (Response, error) {
	var body io.Reader
	if r.Body != nil {
		body = bytes.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, t.BaseURL+r.Path, body)
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("User-Agent", UserAgent)
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	// A JSON body without a declared type is almost always a mistake that
	// makes the app ignore the payload; default it rather than send it bare.
	if req.Header.Get("Content-Type") == "" && strings.HasSuffix(strings.ToLower(r.BodyFile), ".json") {
		req.Header.Set("Content-Type", "application/json")
	}

	start := time.Now()
	resp, err := t.Client.Do(req)
	if err != nil {
		// The caller already names the URL; keep only the underlying cause.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return Response{Duration: time.Since(start)}, err
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseSnippet))
	io.Copy(io.Discard, resp.Body)
	return Response{Status: resp.StatusCode, Duration: time.Since(start), Body: snippet}, nil
}

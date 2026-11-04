// Package effects records the outbound side effects of the application under
// test (e-mails, payment API calls...) through a local HTTP proxy that
// answers with stubs, and checks assertions over the recorded calls.
package effects

import "time"

// MaxBodyBytes is how much of each request body is recorded.
const MaxBodyBytes = 64 << 10

// Call is one outbound request made by the application.
type Call struct {
	Method    string
	Host      string // host name without port
	Path      string
	Query     string
	Body      []byte // at most MaxBodyBytes
	Truncated bool   // the body was longer than MaxBodyBytes
	At        time.Time
}

// Recorder captures outbound calls between resets.
type Recorder interface {
	Reset()
	Calls() []Call
}

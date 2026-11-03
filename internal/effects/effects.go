// Package effects will record the outbound side effects of the application
// under test (e-mails, payment API calls...) through a local HTTP proxy and
// check assertions over them. Not implemented yet: scenario effects are parsed
// and validated but not checked.
package effects

// Call is one outbound request made by the application.
type Call struct {
	Method string
	Host   string
	Path   string
	Body   []byte
}

// Recorder captures outbound calls between deliveries.
type Recorder interface {
	Reset()
	Calls() []Call
}

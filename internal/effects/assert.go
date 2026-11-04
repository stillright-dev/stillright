package effects

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Assertion is a parsed effect assertion such as
//
//	count(host == "api.email.local" && path == "/send") <= 1
//
// The grammar is deliberately tiny and is interpreted, never evaluated as code:
//
//	assertion := "count" "(" [pred] ")" cmp INT
//	pred      := and { "||" and }
//	and       := unary { "&&" unary }
//	unary     := "!" unary | "(" pred ")" | FIELD op STRING
//	FIELD     := method | host | path | query | body
//	op        := "==" | "!=" | "contains"
//	cmp       := "==" | "!=" | "<=" | ">=" | "<" | ">"
type Assertion struct {
	src   string
	pred  node // nil matches every call
	cmp   string
	limit int
}

func (a *Assertion) String() string { return a.src }

// Matching returns the calls the assertion's predicate selects.
func (a *Assertion) Matching(calls []Call) []Call {
	var out []Call
	for _, c := range calls {
		if a.pred == nil || a.pred.eval(c) {
			out = append(out, c)
		}
	}
	return out
}

// Holds reports whether n matching calls satisfy the comparison.
func (a *Assertion) Holds(n int) bool {
	switch a.cmp {
	case "==":
		return n == a.limit
	case "!=":
		return n != a.limit
	case "<=":
		return n <= a.limit
	case ">=":
		return n >= a.limit
	case "<":
		return n < a.limit
	default: // ">"
		return n > a.limit
	}
}

// Expectation renders the comparison, e.g. "<= 1".
func (a *Assertion) Expectation() string { return fmt.Sprintf("%s %d", a.cmp, a.limit) }

// SyntaxError points at the offending column (1-based) of an assertion.
type SyntaxError struct {
	Col int
	Msg string
}

func (e *SyntaxError) Error() string { return fmt.Sprintf("column %d: %s", e.Col, e.Msg) }

// ParseAssertion parses src.
func ParseAssertion(src string) (*Assertion, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	a := &Assertion{src: strings.TrimSpace(src)}

	if err := p.expectWord("count"); err != nil {
		return nil, err
	}
	if err := p.expect("("); err != nil {
		return nil, err
	}
	if p.peek().text != ")" {
		if a.pred, err = p.or(); err != nil {
			return nil, err
		}
	}
	if err := p.expect(")"); err != nil {
		return nil, err
	}
	t := p.next()
	switch t.text {
	case "==", "!=", "<=", ">=", "<", ">":
		a.cmp = t.text
	default:
		return nil, p.errAt(t, "expected a comparison (==, !=, <=, >=, <, >) after count(...)")
	}
	t = p.next()
	if t.kind != tokInt {
		return nil, p.errAt(t, "expected a whole number to compare the count with")
	}
	a.limit, _ = strconv.Atoi(t.text)
	if t := p.peek(); t.kind != tokEOF {
		return nil, p.errAt(t, fmt.Sprintf("unexpected %q after the assertion", t.text))
	}
	return a, nil
}

type node interface{ eval(Call) bool }

type orNode struct{ l, r node }
type andNode struct{ l, r node }
type notNode struct{ n node }
type cmpNode struct {
	field, op, value string
}

func (n orNode) eval(c Call) bool  { return n.l.eval(c) || n.r.eval(c) }
func (n andNode) eval(c Call) bool { return n.l.eval(c) && n.r.eval(c) }
func (n notNode) eval(c Call) bool { return !n.n.eval(c) }

func (n cmpNode) eval(c Call) bool {
	var v string
	switch n.field {
	case "method":
		// Methods are compared case-insensitively: method == "post" works.
		v, n.value = c.Method, strings.ToUpper(n.value)
	case "host":
		v = c.Host
	case "path":
		v = c.Path
	case "query":
		v = c.Query
	case "body":
		v = string(c.Body)
	}
	switch n.op {
	case "==":
		return v == n.value
	case "!=":
		return v != n.value
	default: // contains
		return strings.Contains(v, n.value)
	}
}

var fields = map[string]bool{"method": true, "host": true, "path": true, "query": true, "body": true}

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() token { return p.toks[p.pos] }

func (p *parser) next() token {
	t := p.toks[p.pos]
	if t.kind != tokEOF {
		p.pos++
	}
	return t
}

func (p *parser) errAt(t token, msg string) error {
	if t.kind == tokEOF {
		msg += " (reached the end)"
	}
	return &SyntaxError{Col: t.col, Msg: msg}
}

func (p *parser) expect(text string) error {
	if t := p.next(); t.text != text || t.kind == tokString {
		return p.errAt(t, fmt.Sprintf("expected %q", text))
	}
	return nil
}

func (p *parser) expectWord(w string) error {
	if t := p.next(); t.kind != tokWord || t.text != w {
		return p.errAt(t, fmt.Sprintf("an assertion starts with %s(...)", w))
	}
	return nil
}

func (p *parser) or() (node, error) {
	l, err := p.and()
	for err == nil && p.peek().text == "||" {
		p.next()
		var r node
		if r, err = p.and(); err == nil {
			l = orNode{l, r}
		}
	}
	return l, err
}

func (p *parser) and() (node, error) {
	l, err := p.unary()
	for err == nil && p.peek().text == "&&" {
		p.next()
		var r node
		if r, err = p.unary(); err == nil {
			l = andNode{l, r}
		}
	}
	return l, err
}

func (p *parser) unary() (node, error) {
	t := p.next()
	switch {
	case t.kind == tokOp && t.text == "!":
		n, err := p.unary()
		return notNode{n}, err
	case t.kind == tokOp && t.text == "(":
		n, err := p.or()
		if err != nil {
			return nil, err
		}
		return n, p.expect(")")
	case t.kind == tokWord && fields[t.text]:
		op := p.next()
		if op.text != "==" && op.text != "!=" && !(op.kind == tokWord && op.text == "contains") {
			return nil, p.errAt(op, fmt.Sprintf("expected ==, != or contains after %s", t.text))
		}
		val := p.next()
		if val.kind != tokString {
			return nil, p.errAt(val, `expected a quoted string, like "api.email.local"`)
		}
		return cmpNode{field: t.text, op: op.text, value: val.text}, nil
	case t.kind == tokWord:
		return nil, p.errAt(t, fmt.Sprintf("unknown field %q (use method, host, path, query or body)", t.text))
	default:
		return nil, p.errAt(t, "expected a condition such as host == \"api.email.local\"")
	}
}

type tokKind int

const (
	tokEOF tokKind = iota
	tokWord
	tokString
	tokInt
	tokOp
)

type token struct {
	kind tokKind
	text string
	col  int
}

func lex(src string) ([]token, error) {
	var toks []token
	rs := []rune(src)
	for i := 0; i < len(rs); {
		r := rs[i]
		col := i + 1
		switch {
		case unicode.IsSpace(r):
			i++
		case r == '"':
			var b strings.Builder
			j := i + 1
			for ; j < len(rs) && rs[j] != '"'; j++ {
				if rs[j] == '\\' && j+1 < len(rs) && (rs[j+1] == '"' || rs[j+1] == '\\') {
					j++
				}
				b.WriteRune(rs[j])
			}
			if j >= len(rs) {
				return nil, &SyntaxError{Col: col, Msg: "unterminated string"}
			}
			toks = append(toks, token{tokString, b.String(), col})
			i = j + 1
		case unicode.IsDigit(r):
			j := i
			for j < len(rs) && unicode.IsDigit(rs[j]) {
				j++
			}
			toks = append(toks, token{tokInt, string(rs[i:j]), col})
			i = j
		case unicode.IsLetter(r) || r == '_':
			j := i
			for j < len(rs) && (unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j]) || rs[j] == '_') {
				j++
			}
			toks = append(toks, token{tokWord, string(rs[i:j]), col})
			i = j
		default:
			two := ""
			if i+1 < len(rs) {
				two = string(rs[i : i+2])
			}
			switch {
			case two == "==" || two == "!=" || two == "<=" || two == ">=" || two == "&&" || two == "||":
				toks = append(toks, token{tokOp, two, col})
				i += 2
			case strings.ContainsRune("()<>!", r):
				toks = append(toks, token{tokOp, string(r), col})
				i++
			default:
				return nil, &SyntaxError{Col: col, Msg: fmt.Sprintf("unexpected character %q", r)}
			}
		}
	}
	return append(toks, token{kind: tokEOF, col: len(rs) + 1}), nil
}

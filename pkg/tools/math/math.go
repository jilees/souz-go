// Package math implements the calculator tool: a small hand-rolled
// recursive-descent expression evaluator (no external math library).
package math

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/tools"
)

var _ tools.Tool = (*Calculator)(nil)

// Calculator evaluates arithmetic expressions: + - * / ^, parentheses, unary
// +/-, and the prefix functions sqrt/sin/cos/tan (trig functions take
// degrees, matching how a person would phrase a request in chat).
type Calculator struct{}

func (Calculator) Name() string { return "calculator" }

func (Calculator) Description() string {
	return "Evaluates an arithmetic expression. Supports + - * / ^, parentheses, unary +/-, " +
		"and sqrt/sin/cos/tan (trig functions take degrees). Example: \"(2+3)^2\" -> \"25\"."
}

func (Calculator) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"expression": {"type": "string", "description": "Arithmetic expression, e.g. \"128 * 453\""}
		},
		"required": ["expression"]
	}`)
}

func (Calculator) Execute(_ context.Context, args map[string]json.RawMessage, _ agent.InvocationMeta) (string, error) {
	var expression string
	if raw, ok := args["expression"]; ok {
		if err := json.Unmarshal(raw, &expression); err != nil {
			return "", fmt.Errorf("invalid expression argument: %w", err)
		}
	}

	result, err := Evaluate(expression)
	if err != nil {
		return "", err
	}
	return formatResult(result), nil
}

// formatResult mirrors a calculator's usual display convention: whole
// numbers print without a decimal point, everything else uses Go's default
// float formatting.
func formatResult(v float64) string {
	if v == math.Trunc(v) && !math.IsInf(v, 0) {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// Evaluate parses and computes a single arithmetic expression.
func Evaluate(expr string) (float64, error) {
	p := &parser{input: expr}
	p.skipSpace()
	v, err := p.parseExpr()
	if err != nil {
		return 0, err
	}
	p.skipSpace()
	if p.pos != len(p.input) {
		return 0, fmt.Errorf("unexpected character %q at position %d", p.input[p.pos], p.pos)
	}
	return v, nil
}

// parser is a recursive-descent evaluator over the grammar:
//
//	expr   := term (('+' | '-') term)*
//	term   := unary (('*' | '/') unary)*
//	unary  := ('+' | '-')? power
//	power  := atom ('^' unary)?      // right-associative
//	atom   := number | ident '(' expr ')' | '(' expr ')'
type parser struct {
	input string
	pos   int
}

func (p *parser) skipSpace() {
	for p.pos < len(p.input) && (p.input[p.pos] == ' ' || p.input[p.pos] == '\t') {
		p.pos++
	}
}

func (p *parser) peek() byte {
	if p.pos >= len(p.input) {
		return 0
	}
	return p.input[p.pos]
}

func (p *parser) parseExpr() (float64, error) {
	v, err := p.parseTerm()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		switch p.peek() {
		case '+':
			p.pos++
			rhs, err := p.parseTerm()
			if err != nil {
				return 0, err
			}
			v += rhs
		case '-':
			p.pos++
			rhs, err := p.parseTerm()
			if err != nil {
				return 0, err
			}
			v -= rhs
		default:
			return v, nil
		}
	}
}

func (p *parser) parseTerm() (float64, error) {
	v, err := p.parseUnary()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		switch p.peek() {
		case '*':
			p.pos++
			rhs, err := p.parseUnary()
			if err != nil {
				return 0, err
			}
			v *= rhs
		case '/':
			p.pos++
			rhs, err := p.parseUnary()
			if err != nil {
				return 0, err
			}
			if rhs == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			v /= rhs
		default:
			return v, nil
		}
	}
}

func (p *parser) parseUnary() (float64, error) {
	p.skipSpace()
	switch p.peek() {
	case '+':
		p.pos++
		return p.parseUnary()
	case '-':
		p.pos++
		v, err := p.parseUnary()
		return -v, err
	default:
		return p.parsePower()
	}
}

func (p *parser) parsePower() (float64, error) {
	base, err := p.parseAtom()
	if err != nil {
		return 0, err
	}
	p.skipSpace()
	if p.peek() == '^' {
		p.pos++
		exp, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		return math.Pow(base, exp), nil
	}
	return base, nil
}

func (p *parser) parseAtom() (float64, error) {
	p.skipSpace()
	if p.pos >= len(p.input) {
		return 0, fmt.Errorf("unexpected end of expression")
	}

	c := p.peek()
	switch {
	case c == '(':
		p.pos++
		v, err := p.parseExpr()
		if err != nil {
			return 0, err
		}
		p.skipSpace()
		if p.peek() != ')' {
			return 0, fmt.Errorf("expected ')' at position %d", p.pos)
		}
		p.pos++
		return v, nil
	case c >= '0' && c <= '9' || c == '.':
		return p.parseNumber()
	case isIdentStart(c):
		return p.parseFunctionCall()
	default:
		return 0, fmt.Errorf("unexpected character %q at position %d", c, p.pos)
	}
}

func (p *parser) parseNumber() (float64, error) {
	start := p.pos
	for p.pos < len(p.input) && (p.input[p.pos] >= '0' && p.input[p.pos] <= '9' || p.input[p.pos] == '.') {
		p.pos++
	}
	v, err := strconv.ParseFloat(p.input[start:p.pos], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q", p.input[start:p.pos])
	}
	return v, nil
}

func (p *parser) parseFunctionCall() (float64, error) {
	start := p.pos
	for p.pos < len(p.input) && isIdentPart(p.input[p.pos]) {
		p.pos++
	}
	name := strings.ToLower(p.input[start:p.pos])

	p.skipSpace()
	if p.peek() != '(' {
		return 0, fmt.Errorf("expected '(' after function name %q", name)
	}
	p.pos++
	arg, err := p.parseExpr()
	if err != nil {
		return 0, err
	}
	p.skipSpace()
	if p.peek() != ')' {
		return 0, fmt.Errorf("expected ')' after argument to %q", name)
	}
	p.pos++

	switch name {
	case "sqrt":
		if arg < 0 {
			return 0, fmt.Errorf("sqrt of a negative number")
		}
		return math.Sqrt(arg), nil
	case "sin":
		return math.Sin(arg * math.Pi / 180), nil
	case "cos":
		return math.Cos(arg * math.Pi / 180), nil
	case "tan":
		return math.Tan(arg * math.Pi / 180), nil
	default:
		return 0, fmt.Errorf("unknown function %q", name)
	}
}

func isIdentStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || c >= '0' && c <= '9'
}

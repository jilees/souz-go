package math

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"souz.ru/souz-go/pkg/agent"
)

func TestEvaluate(t *testing.T) {
	cases := []struct {
		expr string
		want float64
	}{
		{"2+3", 5},
		{"128 * 453", 57984},
		{"(2+3)^2", 25},
		{"10 / 4", 2.5},
		{"-5 + 3", -2},
		{"2^3^2", 512}, // right-associative: 2^(3^2)
		{"sqrt(144)", 12},
		{"sin(90)", 1},
		{"cos(0)", 1},
		{"  1 + 1  ", 2},
		{"2 * (3 + (4 - 1))", 12},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Evaluate(tc.expr)
			if err != nil {
				t.Fatalf("Evaluate(%q): %v", tc.expr, err)
			}
			if math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("Evaluate(%q) = %v, want %v", tc.expr, got, tc.want)
			}
		})
	}
}

func TestEvaluate_Errors(t *testing.T) {
	cases := []string{
		"1 +",
		"(1 + 2",
		"1 / 0",
		"sqrt(-1)",
		"2 $ 3",
		"unknown(5)",
		"",
	}
	for _, expr := range cases {
		t.Run(expr, func(t *testing.T) {
			if _, err := Evaluate(expr); err == nil {
				t.Errorf("Evaluate(%q): expected error", expr)
			}
		})
	}
}

func TestCalculator_Execute(t *testing.T) {
	c := Calculator{}
	args := map[string]json.RawMessage{"expression": json.RawMessage(`"3 * 4"`)}
	got, err := c.Execute(context.Background(), args, agent.InvocationMeta{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != "12" {
		t.Errorf("Execute = %q, want %q", got, "12")
	}
}

func TestCalculator_Execute_WholeNumberHasNoDecimalPoint(t *testing.T) {
	c := Calculator{}
	args := map[string]json.RawMessage{"expression": json.RawMessage(`"10 / 2"`)}
	got, err := c.Execute(context.Background(), args, agent.InvocationMeta{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != "5" {
		t.Errorf("Execute = %q, want %q", got, "5")
	}
}

func TestCalculator_Execute_InvalidExpressionReturnsError(t *testing.T) {
	c := Calculator{}
	args := map[string]json.RawMessage{"expression": json.RawMessage(`"1 +"`)}
	if _, err := c.Execute(context.Background(), args, agent.InvocationMeta{}); err == nil {
		t.Fatal("expected error")
	}
}

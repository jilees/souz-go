package graph_test

import (
	"context"
	"errors"
	"testing"

	"souz.ru/souz-go/pkg/graph"
)

func TestRunner_LinearGraph(t *testing.T) {
	double := graph.NewNode("double", func(ctx context.Context, in int) (int, error) {
		return in * 2, nil
	})
	addOne := graph.NewNode("addOne", func(ctx context.Context, in int) (int, error) {
		return in + 1, nil
	})

	def := graph.NewDefinition()
	def.AddEdge(double, addOne)

	runner := &graph.Runner{}
	out, err := runner.Run(context.Background(), double, 5, def, 0, graph.RetryPolicy{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(int); got != 11 {
		t.Errorf("want 11, got %d", got)
	}
}

func TestRunner_ConditionalEdge(t *testing.T) {
	classify := graph.NewNode("classify", func(ctx context.Context, in int) (int, error) {
		return in, nil
	})
	even := graph.NewNode("even", func(ctx context.Context, in int) (int, error) {
		return in * 10, nil
	})
	odd := graph.NewNode("odd", func(ctx context.Context, in int) (int, error) {
		return in*10 + 1, nil
	})

	def := graph.NewDefinition()
	def.AddConditionalEdge(classify, func(out any) bool { return out.(int)%2 == 0 }, even)
	def.AddConditionalEdge(classify, func(out any) bool { return out.(int)%2 != 0 }, odd)

	runner := &graph.Runner{}

	out, err := runner.Run(context.Background(), classify, 4, def, 0, graph.RetryPolicy{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(int); got != 40 {
		t.Errorf("even path: want 40, got %d", got)
	}

	out, err = runner.Run(context.Background(), classify, 3, def, 0, graph.RetryPolicy{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(int); got != 31 {
		t.Errorf("odd path: want 31, got %d", got)
	}
}

func TestRunner_Retry(t *testing.T) {
	attempts := 0
	flaky := graph.NewNode("flaky", func(ctx context.Context, in int) (int, error) {
		attempts++
		if attempts < 3 {
			return 0, errors.New("transient")
		}
		return 42, nil
	})

	def := graph.NewDefinition()
	policy := graph.RetryPolicy{
		MaxAttempts: 3,
		ShouldRetry: func(err error, node *graph.Node, attempt int) bool {
			return err.Error() == "transient"
		},
	}

	runner := &graph.Runner{}
	out, err := runner.Run(context.Background(), flaky, 0, def, 0, policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(int); got != 42 {
		t.Errorf("want 42, got %d", got)
	}
	if attempts != 3 {
		t.Errorf("want 3 attempts, got %d", attempts)
	}
}

func TestRunner_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	noop := graph.NewNode("noop", func(ctx context.Context, in int) (int, error) {
		return in, nil
	})
	def := graph.NewDefinition()
	runner := &graph.Runner{}

	_, err := runner.Run(ctx, noop, 0, def, 0, graph.RetryPolicy{}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}

func TestRunner_MaxSteps(t *testing.T) {
	loop := graph.NewNode("loop", func(ctx context.Context, in int) (int, error) {
		return in + 1, nil
	})
	def := graph.NewDefinition()
	def.AddEdge(loop, loop)

	runner := &graph.Runner{}
	_, err := runner.Run(context.Background(), loop, 0, def, 5, graph.RetryPolicy{}, nil)
	if !errors.Is(err, graph.ErrMaxSteps) {
		t.Errorf("want ErrMaxSteps, got %v", err)
	}
}

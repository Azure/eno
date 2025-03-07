package statespace

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// Model implements a simple fuzz-style test framework.
// Rather than fuzz scalar values like property-based tests, this framework enumerates the subsets of a bounded, high-cardinality state space.
// Essentially, the framework will call the subject with every subset of the known mutations and assert on each invariant.
// Application order of mutations is randomized to avoid order-dependence without fully permuting the space (the implementation is very fast).
type Model[State any, Result any] struct {
	initial     func() State
	subject     func(State) Result
	transitions []mutation[State]
	invariants  []*invariant[State, Result]
}

type invariant[T any, TT any] struct {
	Name   string
	Assert func(T, TT) bool
}

type mutation[T any] struct {
	Name string
	Func func(T) T
}

// Test creates a new model for testing the given subject.
func Test[T any, TT any](fn func(T) TT) *Model[T, TT] { return &Model[T, TT]{subject: fn} }

func (m *Model[T, TT]) WithInitialState(fn func() T) *Model[T, TT] {
	m.initial = fn
	return m
}

// WithMutation appends a function that will be applied to the state while evaluating the model.
func (m *Model[T, TT]) WithMutation(name string, fn func(T) T) *Model[T, TT] {
	m.transitions = append(m.transitions, mutation[T]{Name: name, Func: fn})
	return m
}

// WithInvariant appends a function that will be used to assert on the behavior of the subject for every subset of mutations.
func (m *Model[T, TT]) WithInvariant(name string, fn func(state T, result TT) bool) *Model[T, TT] {
	m.invariants = append(m.invariants, &invariant[T, TT]{Name: name, Assert: fn})
	return m
}

// Evaluate executes the test.
func (m *Model[T, TT]) Evaluate(t *testing.T) {
	m.evaluate(t.Errorf)
}

func (m *Model[T, TT]) evaluate(fail func(msg string, args ...any)) {
	var testCases [][]bool
	for i := range 1 << len(m.transitions) {
		stack := make([]bool, len(m.transitions))
		for j := range m.transitions {
			stack[j] = (i>>j)&1 == 1
		}
		testCases = append(testCases, stack)
	}
	rand.Shuffle(len(testCases), func(i, j int) { testCases[i], testCases[j] = testCases[j], testCases[i] })

	for _, bitmap := range testCases {
		var state T
		if m.initial != nil {
			state = m.initial()
		}

		// Build the state by applying mutations
		for _, i := range rand.Perm(len(bitmap)) {
			if bitmap[i] {
				state = m.transitions[i].Func(state)
			}
		}

		var stack []string
		rand.Shuffle(len(m.invariants), func(i, j int) { m.invariants[i], m.invariants[j] = m.invariants[j], m.invariants[i] })
		for _, inv := range m.invariants {
			if inv.Assert(state, m.subject(state)) {
				continue
			}

			// Defer building the stack strings to avoid allocating the memory for passing tests
			if stack == nil {
				for i, enabled := range bitmap {
					if enabled {
						stack = append(stack, m.transitions[i].Name)
					}
				}
			}

			fail("invariant '%s' failed with mutation stack: [%s]", inv.Name, strings.Join(stack, ", "))
		}
	}
}

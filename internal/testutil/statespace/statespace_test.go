package statespace

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBasics(t *testing.T) {
	failures := []string{}
	subject := func(state int) string { return strconv.Itoa(state) }

	Test(subject).
		WithMutation("increment by one", func(state int) int {
			return state + 1
		}).
		WithMutation("increment by 10", func(state int) int {
			return state + 10
		}).
		WithInvariant("fail on initial", func(_ int, result string) bool {
			return result != "0"
		}).
		WithInvariant("never fail", func(_ int, result string) bool {
			return result != ""
		}).
		WithInvariant("fail when 1", func(_ int, result string) bool {
			return result != "1"
		}).
		WithInvariant("fail when 10", func(state int, result string) bool {
			return result != "10"
		}).
		WithInvariant("fail when 11", func(state int, result string) bool {
			return result != "11"
		}).
		evaluate(func(msg string, args ...any) {
			failures = append(failures, fmt.Sprintf(msg, args...))
		})

	assert.ElementsMatch(t, []string{
		"invariant 'fail on initial' failed with mutation stack: []",
		"invariant 'fail when 1' failed with mutation stack: [increment by one]",
		"invariant 'fail when 10' failed with mutation stack: [increment by 10]",
		"invariant 'fail when 11' failed with mutation stack: [increment by one, increment by 10]",
	}, failures)
}

func TestLargeSpace(t *testing.T) {
	fn := func(bool) bool { return true }

	m := Test(fn)
	for i := 0; i < 1000; i++ {
		m.WithMutation("noop", func(state bool) bool { return state })
	}
	m.Evaluate(t) // just prove it doesn't deadlock or take too long
}

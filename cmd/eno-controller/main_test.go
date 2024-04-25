package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseMapString(t *testing.T) {
	assert.Equal(t, map[string]string{
		"foo": "bar",
		"baz": "a=string=with=equals",
	}, parseMapString("foo=bar,baz=a=string=with=equals,another"))
}

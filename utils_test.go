package main

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestSh_success(t *testing.T) {
	out, err := sh("ls")
	assert.Nil(t, err, formatError("ls", err))
	assert.Contains(t, out, "driver_test.go")
}

func TestSh_fail(t *testing.T) {
	_, err := sh("false")
	assert.NotNil(t, err, formatError("false", err))
}

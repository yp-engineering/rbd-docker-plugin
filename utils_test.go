package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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

func TestShWithDefaultTimeout_triggerDefaultTimeout(t *testing.T) {
	// reset this global for the tests
	defaultShellTimeout = 2 * time.Second

	// sleep long enough to trigger timeout
	sleepSecs := "4"

	// use the default timeout - we want to trigger it
	_, err := shWithDefaultTimeout("sleep", sleepSecs)
	assert.NotNil(t, err, "Expected to get error for timeout")
	assert.Contains(t, err.Error(), "Reached TIMEOUT", "Expected 'Reached TIMEOUT' error")

	// reset
	defaultShellTimeout = 2 * 60 * time.Second
}

func TestShWithTimeout_timeoutZeroFail(t *testing.T) {
	// pass 0 as our duration to trigger the error
	_, err := shWithTimeout(0, "sleep", "1")
	assert.NotNil(t, err, "Expected to get error for duration")
	assert.Contains(t, err.Error(), "duration needs to be positive", "Expected duration validation error")
}

func TestShWithTimeout_cmdSucceeds(t *testing.T) {
	// make command sleep a bit
	sleepSecs := "1"

	// make timeout a bit longer so we don't trigger it
	timeout := 2 * time.Second

	// pass timeout and cmd shorter than that
	_, err := shWithTimeout(timeout, "sleep", sleepSecs)
	assert.Nil(t, err, "Expected success for command, not timeout")
}

func TestShWithTimeout_cmdTimesOut(t *testing.T) {
	// make timeout a bit shorter so we can trigger it
	timeout := 1 * time.Second

	// make command sleep a bit longer than that
	sleepSecs := "4"

	// pass our timeout and long sleep
	_, err := shWithTimeout(timeout, "sleep", sleepSecs)
	assert.NotNil(t, err, "Expected to get error for timeout")
	assert.Contains(t, err.Error(), "Reached TIMEOUT", "Expected 'Reached TIMEOUT' error")
}

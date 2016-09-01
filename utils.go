package main

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"
)

var (
	defaultShellTimeout = 2 * 60 * time.Second
)

// sh is a simple os.exec Command tool, returns trimmed string output
func sh(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if isDebugEnabled() {
		log.Printf("DEBUG: sh CMD: %q", cmd)
	}
	// TODO: capture and output STDERR to logfile?
	out, err := cmd.Output()
	return strings.Trim(string(out), " \n"), err
}

// ShResult used for channel in timeout
type ShResult struct {
	Output string // STDOUT
	Err    error  // go error, not STDERR
}

type ShTimeoutError struct {
	timeout time.Duration
}

func (e ShTimeoutError) Error() string {
	return fmt.Sprintf("Reached TIMEOUT on shell command")
}

// shWithDefaultTimeout will use the defaultShellTimeout so you dont have to pass one
func shWithDefaultTimeout(name string, args ...string) (string, error) {
	return shWithTimeout(defaultShellTimeout, name, args...)
}

// shWithTimeout will run the Cmd and wait for the specified duration
func shWithTimeout(howLong time.Duration, name string, args ...string) (string, error) {
	// duration can't be zero
	if howLong <= 0 {
		return "", fmt.Errorf("Timeout duration needs to be positive")
	}
	// set up the results channel
	resultsChan := make(chan ShResult, 1)
	if isDebugEnabled() {
		log.Printf("DEBUG: shWithTimeout: %v, %s, %v", howLong, name, args)
	}

	// fire up the goroutine for the actual shell command
	go func() {
		out, err := sh(name, args...)
		resultsChan <- ShResult{Output: out, Err: err}
	}()

	select {
	case res := <-resultsChan:
		return res.Output, res.Err
	case <-time.After(howLong):
		return "", ShTimeoutError{timeout: howLong}
	}

	return "", nil
}

// grepLines pulls out lines that match a string (no regex ... yet)
func grepLines(data string, like string) []string {
	var result = []string{}
	if like == "" {
		log.Printf("ERROR: unable to look for empty pattern")
		return result
	}
	like_bytes := []byte(like)

	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		if bytes.Contains(scanner.Bytes(), like_bytes) {
			result = append(result, scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("WARN: error scanning string for %s: %s", like, err)
	}

	return result
}

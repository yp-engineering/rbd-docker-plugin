// Copyright 2015 YP LLC.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.
package main

// unit tests that don't rely on ceph

import (
	"flag"
	"fmt"
	"os"
	"testing"

	dkvolume "github.com/docker/go-plugins-helpers/volume"
	"github.com/stretchr/testify/assert"
)

// TODO: tests that need ceph
// make fake cluster?
// use dockerized container with ceph for tests?

const (
	TEST_SOCKET_PATH             = "/tmp/rbd-test.sock"
	EXPECTED_ACTIVATION_RESPONSE = "{\"Implements\": [\"VolumeDriver\"]}"
)

var (
	testDriver cephRBDVolumeDriver
)

func TestMain(m *testing.M) {
	flag.Parse()
	cephConf := os.Getenv("CEPH_CONF")

	testDriver = newCephRBDVolumeDriver(
		"test",
		"",
		"admin",
		"rbd",
		dkvolume.DefaultDockerRootDirectory,
		cephConf,
		false,
	)
	defer testDriver.shutdown()

	handler := dkvolume.NewHandler(testDriver)
	// Serve won't return so spin off routine
	go handler.ServeUnix("", TEST_SOCKET_PATH)

	os.Exit(m.Run())
}

func TestLocalLockerCookie(t *testing.T) {
	assert.NotEqual(t, "HOST_UNKNOWN", testDriver.localLockerCookie())
}

func TestRbdImageExists_noName(t *testing.T) {
	f_bool, err := testDriver.rbdImageExists(testDriver.pool, "")
	assert.Equal(t, false, f_bool, fmt.Sprintf("%s", err))
}

func TestRbdImageExists_withName(t *testing.T) {
	t.Skip("This fails for many reasons. Need to figure out how to do this in a container.")
	err := testDriver.createRBDImage("rbd", "foo", 1, "xfs")
	assert.Nil(t, err, formatError("createRBDImage", err))
	t_bool, err := testDriver.rbdImageExists(testDriver.pool, "foo")
	assert.Equal(t, true, t_bool, formatError("rbdImageExists", err))
}

// cephRBDDriver.parseImagePoolNameSize(string) (string, string, int, error)
func TestParseImagePoolNameSize_name(t *testing.T) {
	pool, name, size := parseImageAndHandleError(t, "foo")

	assert.Equal(t, testDriver.pool, pool, "Pool should be same")
	assert.Equal(t, "foo", name, "Name should be same")
	assert.Equal(t, *defaultImageSizeMB, size, "Size should be same")
}

func TestParseImagePoolNameSize_complexName(t *testing.T) {
	pool, name, size := parseImageAndHandleError(t, "es-data1_v2.3")

	assert.Equal(t, testDriver.pool, pool, "Pool should be same")
	assert.Equal(t, "es-data1_v2.3", name, "Name should be same")
	assert.Equal(t, *defaultImageSizeMB, size, "Size should be same")
}

func TestParseImagePoolNameSize_withPool(t *testing.T) {
	pool, name, size := parseImageAndHandleError(t, "liverpool/foo")

	assert.Equal(t, "liverpool", pool, "Pool should be same")
	assert.Equal(t, "foo", name, "Name should be same")
	assert.Equal(t, *defaultImageSizeMB, size, "Size should be same")
}

func TestParseImagePoolNameSize_withSize(t *testing.T) {
	pool, name, size := parseImageAndHandleError(t, "liverpool/foo@1024")

	assert.Equal(t, "liverpool", pool, "Pool should be same")
	assert.Equal(t, "foo", name, "Name should be same")
	assert.Equal(t, 1024, size, "Size should be same")
}

func TestParseImagePoolNameSize_withPoolAndSize(t *testing.T) {
	pool, name, size := parseImageAndHandleError(t, "foo@1024")

	assert.Equal(t, testDriver.pool, pool, "Pool should be same")
	assert.Equal(t, "foo", name, "Name should be same")
	assert.Equal(t, 1024, size, "Size should be same")
}

// need a way to test the socket access using basic format - since this broke
// in golang 1.6 with strict Host header checking even if using Unix sockets.
// Requires socat and sudo
//
// Error response when built with golang 1.6: 400 Bad Request: missing required Host header
func TestSocketActivate(t *testing.T) {
	t.Skip("This test requires socket, which seems to need root privs to build. So this test fails if run as normal user. TODO: Find a proper workaround.")
	out, err := sh("bash", "-c", "echo \"POST /Plugin.Activate HTTP/1.1\r\n\" | sudo socat unix-connect:/tmp/rbd-test.sock STDIO")
	assert.Nil(t, err, formatError("socat plugin activate", err))
	assert.Contains(t, out, EXPECTED_ACTIVATION_RESPONSE, "Expecting Implements VolumeDriver message")

}

// Helpers
func formatError(name string, err error) string {
	return fmt.Sprintf("ERROR calling %s: %q", name, err)
}

func parseImageAndHandleError(t *testing.T, name string) (string, string, int) {
	pool, name, size, err := testDriver.parseImagePoolNameSize(name)
	assert.Nil(t, err, formatError("parseImagePoolNameSize", err))
	return pool, name, size
}

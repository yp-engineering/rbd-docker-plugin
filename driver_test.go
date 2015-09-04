// Copyright 2015 YP LLC.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.
package main

// unit tests that don't rely on ceph

import (
	"fmt"
	"os"
	"testing"

	"github.com/calavera/dkvolume"
	"github.com/stretchr/testify/assert"
)

// TODO: tests that need ceph
// make fake cluster?
// use dockerized container with ceph for tests?

var (
	testDriver cephRBDVolumeDriver
)

func TestMain(m *testing.M) {
	cephConf := os.Getenv("CEPH_CONF")

	testDriver = newCephRBDVolumeDriver(
		"test",
		"",
		"admin",
		"rbd",
		dkvolume.DefaultDockerRootDirectory,
		cephConf,
	)
	defer testDriver.shutdown()

	os.Exit(m.Run())
}

func TestDriverReload(t *testing.T) {
	t.Skip("This causes an error at driver.go:755 rbdImage.Open()")
	testDriver.reload()
}

func TestLocalLockerCookie(t *testing.T) {
	assert.NotEqual(t, "HOST_UNKNOWN", testDriver.localLockerCookie())
}

func TestRbdImageExists_noName(t *testing.T) {
	f_bool, err := testDriver.rbdImageExists(testDriver.defaultPool, "")
	assert.Equal(t, false, f_bool, fmt.Sprintf("%s", err))
}

func TestRbdImageExists_withName(t *testing.T) {
	t.Skip("This fails for many reasons. Need to figure out how to do this in a container.")
	err := testDriver.createRBDImage("rbd", "foo", 1, "xfs")
	assert.Nil(t, err, formatError("createRBDImage", err))
	t_bool, err := testDriver.rbdImageExists(testDriver.defaultPool, "foo")
	assert.Equal(t, true, t_bool, formatError("rbdImageExists", err))
}

// cephRBDDriver.parseImagePoolNameSize(string) (string, string, int, error)
func TestParseImagePoolNameSize_name(t *testing.T) {
	pool, name, size := parseImageAndHandleError(t, "foo")

	assert.Equal(t, testDriver.defaultPool, pool, "Pool should be same")
	assert.Equal(t, "foo", name, "Name should be same")
	assert.Equal(t, *defaultImageSizeMB, size, "Size should be same")
}

func TestParseImagePoolNameSize_complexName(t *testing.T) {
	pool, name, size := parseImageAndHandleError(t, "es-data1_v2.3")

	assert.Equal(t, testDriver.defaultPool, pool, "Pool should be same")
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

	assert.Equal(t, testDriver.defaultPool, pool, "Pool should be same")
	assert.Equal(t, "foo", name, "Name should be same")
	assert.Equal(t, 1024, size, "Size should be same")
}

func TestSh_success(t *testing.T) {
	out, err := sh("ls")
	assert.Nil(t, err, formatError("sh", err))
	assert.Contains(t, out, "driver_test.go")
}

func TestSh_fail(t *testing.T) {
	_, err := sh("false")
	assert.NotNil(t, err, formatError("false", err))
}

// Helpers
func formatError(name string, err error) string {
	return fmt.Sprintf("ERROR calling %s: %s", name, err)
}

func parseImageAndHandleError(t *testing.T, name string) (string, string, int) {
	pool, name, size, err := testDriver.parseImagePoolNameSize(name)
	assert.Nil(t, err, formatError("parseImagePoolNameSize", err))
	return pool, name, size
}

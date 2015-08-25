// +build ceph

// These tests are functional tests against a real ceph instance.
package main

import (
	"fmt"
	"os"

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

func TestSh_success(t *testing.T) {
	out, err := sh("ls")
	assert.Nil(t, err, formatError("sh", err))
	assert.Contains(t, out, "driver_test.go")
}

func TestSh_fail(t *testing.T) {
	_, err := sh("false")
	assert.NotNil(t, err, formatError("false", err))
}

func TestRbdImageExists_withName(t *testing.T) {
	// Fails because can't mount into docker image cause lack of kernel headers.
	err := testDriver.createRBDImage("rbd", "foo", 1, "xfs")
	assert.Nil(t, err, formatError("createRBDImage", err))
	t_bool, err := testDriver.rbdImageExists(testDriver.defaultPool, "foo")
	assert.Equal(t, true, t_bool, fmt.Sprintf("%s", err))
}

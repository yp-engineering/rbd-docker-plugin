// Copyright 2015 YP Holdings LLC.
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

func TestRbdImageExists_noName(t *testing.T) {
	f_bool, err := testDriver.rbdImageExists(testDriver.defaultPool, "")
	assert.Equal(t, false, f_bool, fmt.Sprintf("%s", err))
}

func TestRbdImageExists_withName(t *testing.T) {
	testDriver.createRBDImage("rbd", "foo", 1, "xfs")
	t_bool, err := testDriver.rbdImageExists(testDriver.defaultPool, "foo")
	assert.Equal(t, true, t_bool, fmt.Sprintf("%s", err))
}

// cephRBDDriver.parseImagePoolNameSize(string) (string, string, int, error)
func TestParseImagePoolNameSize_name(t *testing.T) {
	pool, name, size, err := testDriver.parseImagePoolNameSize("foo")
	if err != nil {
		t.Errorf("ERROR calling parseImagePoolNameSize: %s", err)
	}

	assert.Equal(t, testDriver.defaultPool, pool, "Pool should be same")
	assert.Equal(t, "foo", name, "Name should be same")
	assert.Equal(t, *defaultImageSizeMB, size, "Size should be same")
}

func TestParseImagePoolNameSize_complexName(t *testing.T) {
	pool, name, size, err := testDriver.parseImagePoolNameSize("es-data1_v2.3")
	if err != nil {
		t.Errorf("ERROR calling parseImagePoolNameSize: %s", err)
	}
	assert.Equal(t, testDriver.defaultPool, pool, "Pool should be same")
	assert.Equal(t, "es-data1_v2.3", name, "Name should be same")
	assert.Equal(t, *defaultImageSizeMB, size, "Size should be same")
}

func TestParseImagePoolNameSize_withPool(t *testing.T) {
	pool, name, size, err := testDriver.parseImagePoolNameSize("liverpool/foo")
	if err != nil {
		t.Errorf("ERROR calling parseImagePoolNameSize: %s", err)
	}
	assert.Equal(t, "liverpool", pool, "Pool should be same")
	assert.Equal(t, "foo", name, "Name should be same")
	assert.Equal(t, *defaultImageSizeMB, size, "Size should be same")
}

func TestParseImagePoolNameSize_withSize(t *testing.T) {
	pool, name, size, err := testDriver.parseImagePoolNameSize("liverpool/foo@1024")
	if err != nil {
		t.Errorf("ERROR calling parseImagePoolNameSize: %s", err)
	}
	assert.Equal(t, "liverpool", pool, "Pool should be same")
	assert.Equal(t, "foo", name, "Name should be same")
	assert.Equal(t, 1024, size, "Size should be same")
}

func TestParseImagePoolNameSize_withPoolAndSize(t *testing.T) {
	pool, name, size, err := testDriver.parseImagePoolNameSize("foo@1024")
	if err != nil {
		t.Errorf("ERROR calling parseImagePoolNameSize: %s", err)
	}
	assert.Equal(t, testDriver.defaultPool, pool, "Pool should be same")
	assert.Equal(t, "foo", name, "Name should be same")
	assert.Equal(t, 1024, size, "Size should be same")
}

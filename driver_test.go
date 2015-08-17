// Copyright 2015 YP Holdings LLC.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.
package main

// unit tests that don't rely on ceph

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TODO: tests that need ceph
// make fake cluster?
// use dockerized container with ceph for tests?

var (
	testDriver cephRBDVolumeDriver
)

func TestMain(m *testing.M) {
	// make an empty driver ...
	testDriver = cephRBDVolumeDriver{
		name:        "test",
		defaultPool: "testpool",
	}

	os.Exit(m.Run())
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

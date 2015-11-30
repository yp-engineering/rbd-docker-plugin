// Copyright 2015 YP LLC.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.
package main

// trying to write a small test to reproduce (un)locking issues

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TODO: tests that need ceph
// make fake cluster?
// use dockerized container with ceph for tests?

var (
//testDriver cephRBDVolumeDriver
)

func TestRbdImageExists_withReconnect(t *testing.T) {
	var err error
	var image = "foo2"

	// connect to default RBD pool
	err = testDriver.connect("rbd")
	assert.Nil(t, err, err)
	defer testDriver.shutdown()

	// make a new image
	err = testDriver.createRBDImage("rbd", image, 1, "xfs")
	assert.Nil(t, err, formatError("createRBDImage", err))

	// check that it exists
	t_bool, err := testDriver.rbdImageExists(testDriver.defaultPool, image)
	assert.Equal(t, true, t_bool, formatError("rbdImageExists", err))

	// lock it

	// reconnect

	// check that it exists again (e.g. in order to unlock it)
}

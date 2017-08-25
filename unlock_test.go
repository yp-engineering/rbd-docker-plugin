// Copyright 2015 YP LLC.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.
package main

// trying to write a small test to reproduce (un)locking issues

import (
	"bytes"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"testing"

	//"github.com/stretchr/testify/assert"
	assert "github.com/stretchr/testify/require"
)

//"github.com/ceph/go-ceph/rbd"
//"github.com/ceph/go-ceph/rados"

var (
	//testDriver cephRBDVolumeDriver
	testPool  = "rbd"
	testImage = "rbd-test"
)

func TestShUnlockImage(t *testing.T) {
	// lock it first ... ?
	locker, err := testDriver.lockImage(testPool, testImage)
	if err != nil {
		log.Printf("WARN: Unable to lock image in preparation for test: %s", err)
		locker = testDriver.localLockerCookie()
	}

	// now unlock it
	err = testDriver.unlockImage(testPool, testImage, locker)
	assert.Nil(t, err, fmt.Sprintf("Unable to unlock image using sh rbd: %s", err))
}

func test_sh(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	log.Printf("DEBUG: sh CMD: %q", cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		log.Printf("ERROR: %q: %s", err, stderr)
	}
	return strings.Trim(string(out), " \n"), err
}

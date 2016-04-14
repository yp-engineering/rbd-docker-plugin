// Copyright 2015 YP LLC.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.
package main

// trying to write a small test to reproduce (un)locking issues

import (
	"bytes"
	"fmt"
	"log"
	"os"
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

func TestGoCephConnection(t *testing.T) {
	var err error

	config := os.Getenv("CEPH_CONF")

	// connect to default RBD pool
	err = testDriver.connect(testPool)
	assert.Nil(t, err, err)
	defer testDriver.shutdown()

	// check if we need to make the image
	imageAlreadyExists, err := testDriver.rbdImageExists(testPool, testImage)
	assert.Nil(t, err, fmt.Sprintf("Unable to check if image already exists: %s", err))

	if imageAlreadyExists {
		log.Printf("NOTE: image already exists: %s", testImage)
	} else {
		// make an image and format it  - do this via command line because ...
		// to avoid issues with go-ceph and/or our implementation using it
		_, err = test_sh("rbd", "--conf", config, "create", testImage, "--size", "1024")
		assert.Nil(t, err, fmt.Sprintf("Unable to create new image: %s", err))

		// FIXME: TODO: this is hanging for some reason ?? why --
		testDevice, err := test_sh("sudo", "rbd", "--conf", config, "--pool", testPool, "map", testImage)
		// try other shell ... never hung before ?
		//testDevice, err := testDriver.mapImage(testPool, testImage)
		assert.Nil(t, err, fmt.Sprintf("Unable to map image: %s", err))
		assert.NotEqual(t, testDevice, "", fmt.Sprintf("Got an empty device name: '%s'", testDevice))

		out, err := test_sh("sudo", "mkfs.xfs", testDevice)
		log.Printf("DEBUG: mkfs output: ...\n%s", out)
		assert.Nil(t, err, fmt.Sprintf("Unable to mkfs.xfs: %s", err))

		// unmap device to get ready to use via go-ceph lib
		_, err = test_sh("sudo", "rbd", "--conf", config, "--pool", testPool, "unmap", testDevice)
		assert.Nil(t, err, fmt.Sprintf("Unable to unmap new fs image: %s", err))
	}

	//****************************************
	// now try some go-ceph func

	// check that it exists
	t1_bool, err := testDriver.rbdImageExists(testPool, testImage)
	assert.Equal(t, true, t1_bool, fmt.Sprintf("Unable to find image after create: %s", err))

	// try an unlock image - just in case ?
	/**
	err = testDriver.unlockImage(testPool, testImage, "")
	if err != nil {
		log.Printf("Expected failure to unlock image, but maybe for wrong reason: %s", err)
	} else {
		log.Printf("Expected failure didn't fail: image was already locked and we unlocked it.")
	}
	*/

	// check that it exists (again)
	t2_bool, err := testDriver.rbdImageExists(testPool, testImage)
	assert.Equal(t, true, t2_bool, fmt.Sprintf("Unable to find image after create: %s", err))

	// lock image
	locker, err := testDriver.lockImage(testPool, testImage)
	assert.Nil(t, err, fmt.Sprintf("Unable to get exclusive lock on image: %s", err))
	assert.NotEqual(t, locker, "", fmt.Sprintf("Got an empty Locker name: '%s'", locker))

	// can we list the lockers ? can we even get a valid open image handle?
	/** big fat panic deep in go-ceph c-lib interaction
	img := rbd.GetImage(testDriver.ioctx, testImage)
	err = img.Open(true)
	assert.Nil(t, err, fmt.Sprintf("Unable to open image via go-ceph: %s", err))

	tag, lockers, err := img.ListLockers()
	assert.Nil(t, err, fmt.Sprintf("Unable to list lockers for image: %s", err))
	log.Printf("GetImage list lockers results: tag=%s, lockers=%q", tag, lockers)
	*/

	// shutdown / reconnect the ceph client
	testDriver.shutdown()
	err = testDriver.connect(testPool)
	assert.Nil(t, err, fmt.Sprintf("Error reconnecting: %s", err))

	// check that it exists again (e.g. in order to unlock it)
	t3_bool, err := testDriver.rbdImageExists(testPool, testImage)
	assert.Equal(t, true, t3_bool, fmt.Sprintf("Unable to find image after create: %s", err))

	// unlock image
	err = testDriver.unlockImage(testPool, testImage, locker)
	assert.Nil(t, err, fmt.Sprintf("Unable to unlock image: %s", err))

	// check that it exists again (e.g. because sanity)
	t4_bool, err := testDriver.rbdImageExists(testPool, testImage)
	assert.Equal(t, true, t4_bool, fmt.Sprintf("Unable to find image after create: %s", err))
}

func TestShUnlockImage(t *testing.T) {
	// lock it first ... ?
	locker, err := testDriver.sh_lockImage(testPool, testImage)
	if err != nil {
		log.Printf("WARN: Unable to lock image in preparation for test: %s", err)
		locker = testDriver.localLockerCookie()
	}

	// now unlock it
	err = testDriver.sh_unlockImage(testPool, testImage, locker)
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

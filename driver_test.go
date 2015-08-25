// Copyright 2015 YP Holdings LLC.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.
package main

// unit tests that don't rely on ceph

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

// http://stackoverflow.com/a/6878625
const MaxUint = ^uint(0)
const MaxInt = int(^uint(0) >> 1)

func TestParseImagePoolNameSize(t *testing.T) {
	td := cephRBDVolumeDriver{}
	dp := "mypool"
	td.defaultPool = dp
	s := *defaultImageSizeMB

	tcs := []struct {
		input string
		pool  string
		name  string
		size  int
		err   error
	}{
		{"foo", dp, "foo", s, nil},
		{"es-data1_v2.3", dp, "es-data1_v2.3", s, nil},
		{"liverpool/foo", "liverpool", "foo", s, nil},
		{"liverpool/foo@1024", "liverpool", "foo", 1024, nil},
		{"foo@1024", dp, "foo", 1024, nil},
		{"foo@", "", "", 0, fmt.Errorf("Unable to parse image name: foo@")},
		// Max int converts to default
		{"foo@" + strconv.Itoa(MaxInt) + "0", dp, "foo", s, nil},
	}

	for _, tc := range tcs {
		pool, name, size, err := td.parseImagePoolNameSize(tc.input)

		assert.Equal(t, tc.err, err, "%s: error (%v)", tc.input, err)
		assert.Equal(t, tc.pool, pool, "input %s: pool %#v wanted, got %#v", tc.input, tc.pool, pool)
		assert.Equal(t, tc.name, name, "input %s: name %#v wanted, got %#v", tc.input, tc.name, name)
		assert.Equal(t, tc.size, size, "input %s: size %#d wanted, got %#d", tc.input, tc.size, size)
	}
}

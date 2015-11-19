// Copyright 2015 YP LLC.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.
package main

// Ceph RBD Docker VolumeDriver Plugin
//
// Run rbd-docker-plugin service, which creates a socket that can accept JSON
// HTTP POSTs from docker engine.
//
// System Requirements:
//   - requires ceph, rados and rbd development headers to go get go-ceph
//     yum install ceph-devel librados2-devel librbd1-devel
//
// Plugin name: rbd  (yp-rbd? ceph-rbd?) -- now configurable via --name
//
// 	docker run --volume-driver=rbd -v imagename:/mnt/dir IMAGE [CMD]
//
// golang github code examples:
// - https://github.com/docker/docker/blob/master/experimental/plugins_volume.md
// - https://github.com/noahdesu/go-ceph
// - https://github.com/calavera/dkvolume
// - https://github.com/calavera/docker-volume-glusterfs
// - https://github.com/AcalephStorage/docker-volume-ceph-rbd

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/calavera/dkvolume"
	"github.com/noahdesu/go-ceph/rados"
	"github.com/noahdesu/go-ceph/rbd"
)

// TODO: switch to github.com/ceph/go-ceph ?
// TODO: use versioned dependencies -- e.g. newest dkvolume already has breaking changes?

var (
	imageNameRegexp = regexp.MustCompile(`^(([-_.[:alnum:]]+)/)?([-_.[:alnum:]]+)(@([0-9]+))?$`) // optional pool or size in image name
)

// Volume is the Docker concept which we map onto a Ceph RBD Image
type volume struct {
	name   string // RBD Image name
	device string // local host kernel device (e.g. /dev/rbd1)
	locker string // track the lock name
	fstype string
	pool   string
}

type cephRBDVolumeDriver struct {
	// - using default ceph cluster name ("ceph")
	// - using default ceph config (/etc/ceph/<cluster>.conf)
	//
	// TODO: when starting, what if there are mounts already for RBD devices?
	// do we ingest them as our own or ... currently fails if locked
	//
	// TODO: use a chan as semaphore instead of mutex in driver?

	name         string             // unique name for plugin
	cluster      string             // ceph cluster to use (default: ceph)
	user         string             // ceph user to use (default: admin)
	defaultPool  string             // default ceph pool to use (default: rbd)
	root         string             // scratch dir for mounts for this plugin
	config       string             // ceph config file to read
	volumes      map[string]*volume // track locally mounted volumes
	m            *sync.Mutex        // mutex to guard operations that change volume maps or use conn
	conn         *rados.Conn        // keep an open connection
	defaultIoctx *rados.IOContext   // context for default pool
}

// newCephRBDVolumeDriver builds the driver struct, reads config file and connects to cluster
func newCephRBDVolumeDriver(pluginName, cluster, userName, defaultPoolName, rootBase, config string) cephRBDVolumeDriver {
	// the root mount dir will be based on docker default root and plugin name - pool added later per volume
	mountDir := filepath.Join(rootBase, pluginName)
	log.Printf("INFO: newCephRBDVolumeDriver: setting base mount dir=%s", mountDir)

	// fill everything except the connection and context
	driver := cephRBDVolumeDriver{
		name:        pluginName,
		cluster:     cluster,
		user:        userName,
		defaultPool: defaultPoolName,
		root:        mountDir,
		config:      config,
		volumes:     map[string]*volume{},
		m:           &sync.Mutex{},
	}
	//conn:        cephConn,

	driver.connect()

	return driver
}

// ************************************************************
//
// Implement the Docker VolumeDriver API via dkvolume interface
//
// Using https://github.com/calavera/dkvolume
//
// ************************************************************

// Create will ensure the RBD image requested is available.  Plugin requires
// --create option to provision new RBD images.
//
// POST /VolumeDriver.Create
//
// Request:
//    { "Name": "volume_name" }
//    Instruct the plugin that the user wants to create a volume, given a user
//    specified volume name. The plugin does not need to actually manifest the
//    volume on the filesystem yet (until Mount is called).
//
// Response:
//    { "Err": null }
//    Respond with a string error if an error occurred.
//
func (d cephRBDVolumeDriver) Create(r dkvolume.Request) dkvolume.Response {
	log.Printf("INFO: API Create(%s)", r.Name)
	d.m.Lock()
	defer d.m.Unlock()

	// parse image name optional/default pieces
	pool, name, size, err := d.parseImagePoolNameSize(r.Name)
	if err != nil {
		log.Printf("ERROR: parsing volume: %s", err)
		return dkvolume.Response{Err: err.Error()}
	}

	mount := d.mountpoint(pool, name)

	// do we already know about this volume? return early
	if _, found := d.volumes[mount]; found {
		log.Println("INFO: Volume is already in known mounts: " + mount)
		return dkvolume.Response{}
	}

	// otherwise, check ceph rbd api for it
	exists, err := d.rbdImageExists(pool, name)
	if err != nil {
		log.Printf("ERROR: checking for RBD Image: %s", err)
		return dkvolume.Response{Err: err.Error()}
	}
	if !exists {
		if !*canCreateVolumes {
			errString := fmt.Sprintf("Ceph RBD Image not found: %s", name)
			log.Println("ERROR: " + errString)
			return dkvolume.Response{Err: errString}
		}
		// try to create it ... use size and default fs-type
		err = d.createRBDImage(pool, name, size, *defaultImageFSType)
		if err != nil {
			errString := fmt.Sprintf("Unable to create Ceph RBD Image(%s): %s", name, err)
			log.Println("ERROR: " + errString)
			return dkvolume.Response{Err: errString}
		}
	}

	return dkvolume.Response{}
}

// TODO: figure out when this is called in docker/mesos/marathon cycle
// ... does this only get called explicitly via docker rm -v ...? and so if
// you call that you actually expect to destroy the volume?
//
// POST /VolumeDriver.Remove
//
// Request:
//    { "Name": "volume_name" }
//    Remove a volume, given a user specified volume name.
//
// Response:
//    { "Err": null }
//    Respond with a string error if an error occurred.
//
func (d cephRBDVolumeDriver) Remove(r dkvolume.Request) dkvolume.Response {
	log.Printf("INFO: API Remove(%s)", r.Name)
	d.m.Lock()
	defer d.m.Unlock()

	// parse full image name for optional/default pieces
	pool, name, _, err := d.parseImagePoolNameSize(r.Name)
	if err != nil {
		log.Printf("ERROR: parsing volume: %s", err)
		return dkvolume.Response{Err: err.Error()}
	}

	mount := d.mountpoint(pool, name)

	// do we know about this volume? does it matter?
	if _, found := d.volumes[mount]; !found {
		log.Printf("WARN: Volume is not in known mounts: %s", mount)
	}

	// check ceph rbd api for it
	exists, err := d.rbdImageExists(pool, name)
	if err != nil {
		log.Printf("ERROR: checking for RBD Image: %s", err)
		return dkvolume.Response{Err: err.Error()}
	}
	if !exists {
		errString := fmt.Sprintf("Ceph RBD Image not found: %s", name)
		log.Println("ERROR: " + errString)
		return dkvolume.Response{Err: errString}
	}

	// attempt to gain lock before remove - lock disappears after rm or rename
	locker, err := d.lockImage(pool, name)
	if err != nil {
		errString := fmt.Sprintf("Unable to lock image for remove: %s", name)
		log.Println("ERROR: " + errString)
		return dkvolume.Response{Err: errString}
	}

	if *canRemoveVolumes {
		// remove it (for real - destroy it ... )
		err = d.removeRBDImage(pool, name)
		if err != nil {
			errString := fmt.Sprintf("Unable to remove Ceph RBD Image(%s): %s", name, err)
			log.Println("ERROR: " + errString)
			defer d.unlockImage(pool, name, locker)
			return dkvolume.Response{Err: errString}
		}
	} else {
		// just rename it (in case needed later, or can be culled via script)
		err = d.renameRBDImage(pool, name, "zz_"+name)
		if err != nil {
			errString := fmt.Sprintf("Unable to rename with zz_ prefix: RBD Image(%s): %s", name, err)
			log.Println("ERROR: " + errString)
			defer d.unlockImage(pool, name, locker)
			return dkvolume.Response{Err: errString}
		}
	}

	delete(d.volumes, mount)
	return dkvolume.Response{}
}

// Mount will Ceph Map the RBD image to the local kernel and create a mount
// point and mount the image.
//
// POST /VolumeDriver.Mount
//
// Request:
//    { "Name": "volume_name" }
//    Docker requires the plugin to provide a volume, given a user specified
//    volume name. This is called once per container start.
//
// Response:
//    { "Mountpoint": "/path/to/directory/on/host", "Err": null }
//    Respond with the path on the host filesystem where the volume has been
//    made available, and/or a string error if an error occurred.
//
func (d cephRBDVolumeDriver) Mount(r dkvolume.Request) dkvolume.Response {
	log.Printf("INFO: API Mount(%s)", r.Name)
	d.m.Lock()
	defer d.m.Unlock()

	// parse full image name for optional/default pieces
	pool, name, _, err := d.parseImagePoolNameSize(r.Name)
	if err != nil {
		log.Printf("ERROR: parsing volume: %s", err)
		return dkvolume.Response{Err: err.Error()}
	}

	mount := d.mountpoint(pool, name)

	// FIXME: this is failing - see error below - for now we just attempt to grab a lock
	//
	// check that the image is not locked already
	//locked, err := d.rbdImageIsLocked(name)
	//if locked || err != nil {
	//	log.Printf("ERROR: checking for RBD Image(%s) lock: %s", name, err)
	//	return dkvolume.Response{Err: "RBD Image locked"}
	//}

	locker, err := d.lockImage(pool, name)
	if err != nil {
		log.Printf("ERROR: locking RBD Image(%s): %s", name, err)
		return dkvolume.Response{Err: "Unable to get Exlusive Lock"}
	}

	// map and mount the RBD image -- these are OS level commands, not avail in go-ceph

	// map
	device, err := d.mapImage(pool, name)
	if err != nil {
		log.Printf("ERROR: mapping RBD Image(%s) to kernel device: %s", name, err)
		// failsafe: need to release lock
		defer d.unlockImage(pool, name, locker)
		return dkvolume.Response{Err: "Unable to map"}
	}

	// determine device FS type
	fstype, err := d.deviceType(device)
	if err != nil {
		log.Printf("WARN: unable to detect RBD Image(%s) fstype: %s", name, err)
		// NOTE: don't fail - FOR NOW we will assume default plugin fstype
		fstype = *defaultImageFSType
	}

	// check for mountdir - create if necessary
	err = os.MkdirAll(mount, os.ModeDir|os.FileMode(int(0775)))
	if err != nil {
		log.Printf("ERROR: creating mount directory: %s", err)
		// failsafe: need to release lock and unmap kernel device
		defer d.unmapImageDevice(device)
		defer d.unlockImage(pool, name, locker)
		return dkvolume.Response{Err: "Unable to make mountdir"}
	}

	// mount
	err = d.mountDevice(device, mount, fstype)
	if err != nil {
		log.Printf("ERROR: mounting device(%s) to directory(%s): %s", device, mount, err)
		// need to release lock and unmap kernel device
		defer d.unmapImageDevice(device)
		defer d.unlockImage(pool, name, locker)
		return dkvolume.Response{Err: "Unable to mount device"}
	}

	// if all that was successful - add to our list of volumes
	d.volumes[mount] = &volume{
		name:   name,
		device: device,
		locker: locker,
		fstype: fstype,
		pool:   pool,
	}

	return dkvolume.Response{Mountpoint: mount}
}

// Path returns the path to host directory mountpoint for volume.
//
// POST /VolumeDriver.Path
//
// Request:
//    { "Name": "volume_name" }
//    Docker needs reminding of the path to the volume on the host.
//
// Response:
//    { "Mountpoint": "/path/to/directory/on/host", "Err": null }
//    Respond with the path on the host filesystem where the volume has been
//    made available, and/or a string error if an error occurred.
//
func (d cephRBDVolumeDriver) Path(r dkvolume.Request) dkvolume.Response {
	// parse full image name for optional/default pieces
	pool, name, _, err := d.parseImagePoolNameSize(r.Name)
	if err != nil {
		log.Printf("ERROR: parsing volume: %s", err)
		return dkvolume.Response{Err: err.Error()}
	}

	mountPath := d.mountpoint(pool, name)
	log.Printf("INFO: API Path request(%s) => %s", name, mountPath)
	return dkvolume.Response{Mountpoint: mountPath}
}

// POST /VolumeDriver.Unmount
//
// - assuming writes are finished and no other containers using same disk on this host?

// Request:
//    { "Name": "volume_name" }
//    Indication that Docker no longer is using the named volume. This is
//    called once per container stop. Plugin may deduce that it is safe to
//    deprovision it at this point.
//
// Response:
//    { "Err": null }
//    Respond with a string error if an error occurred.
//
func (d cephRBDVolumeDriver) Unmount(r dkvolume.Request) dkvolume.Response {
	log.Printf("INFO: API Unmount(%s)", r.Name)
	d.m.Lock()
	defer d.m.Unlock()

	var err_msgs = []string{}

	// parse full image name for optional/default pieces
	pool, name, _, err := d.parseImagePoolNameSize(r.Name)
	if err != nil {
		log.Printf("ERROR: parsing volume: %s", err)
		return dkvolume.Response{Err: err.Error()}
	}

	mount := d.mountpoint(pool, name)

	// check if it's in our mounts - we may not know about it if plugin was started late?
	vol, found := d.volumes[mount]
	if !found {
		log.Println("WARN: Volume is not in known mounts: will attempt limited Unmount: " + name)
		// set up a fake Volume with defaults ...
		// - device is /dev/rbd/<pool>/<image> in newer ceph versions
		// - assume we are the locker (will fail if locked from another host)
		vol = &volume{
			pool:   pool,
			name:   name,
			device: fmt.Sprintf("/dev/rbd/%s/%s", pool, name),
			locker: d.localLockerCookie(),
		}
	}

	// unmount
	err = d.unmountDevice(vol.device)
	if err != nil {
		log.Printf("ERROR: unmounting device(%s): %s", vol.device, err)
		// failsafe: will still attempt to unmap and unlock
		err_msgs = append(err_msgs, "Error unmounting device")
	}

	// unmap
	err = d.unmapImageDevice(vol.device)
	if err != nil {
		log.Printf("ERROR: unmapping image device(%s): %s", vol.device, err)
		// failsafe: attempt to unlock
		err_msgs = append(err_msgs, "Error unmapping kernel device")
	}

	// unlock
	err = d.unlockImage(vol.pool, vol.name, vol.locker)
	if err != nil {
		log.Printf("ERROR: unlocking RBD image(%s): %s", vol.name, err)
		err_msgs = append(err_msgs, "Error unlocking image")
	}

	// forget it
	delete(d.volumes, mount)

	// check for piled up errors
	if len(err_msgs) > 0 {
		return dkvolume.Response{Err: strings.Join(err_msgs, ", ")}
	}

	return dkvolume.Response{}
}

// END Docker VolumeDriver Plugin API methods
// ***************************************************************************

//

// shutdown closes the connection - maybe not needed unless we recreate conn?
// more info:
// - https://github.com/noahdesu/go-ceph/blob/master/rados/ioctx.go#L127
// - http://ceph.com/docs/master/rados/api/librados/
func (d *cephRBDVolumeDriver) shutdown() {
	log.Println("INFO: Ceph RBD Driver shutdown() called")
	d.m.Lock()
	defer d.m.Unlock()

	if d.defaultIoctx != nil {
		d.defaultIoctx.Destroy()
	}
	if d.conn != nil {
		d.conn.Shutdown()
	}
}

// reload will try to (re)connect to ceph
func (d *cephRBDVolumeDriver) reload() {
	log.Println("INFO: Ceph RBD Driver reload() called")
	d.shutdown()
	d.connect()
}

// connect builds up the ceph conn and default pool
func (d *cephRBDVolumeDriver) connect() {
	log.Println("INFO: connecting to Ceph and default pool context")
	d.m.Lock()
	defer d.m.Unlock()

	// create reusable go-ceph Client Connection
	var cephConn *rados.Conn
	var err error
	if d.cluster == "" {
		cephConn, err = rados.NewConnWithUser(d.user)
	} else {
		// FIXME: TODO: can't seem to use a cluster name -- get error -22 from go-ceph/rados:
		// panic: Unable to create ceph connection to cluster=ceph with user=admin: rados: ret=-22
		cephConn, err = rados.NewConnWithClusterAndUser(d.cluster, d.user)
	}
	if err != nil {
		log.Panicf("ERROR: Unable to create ceph connection to cluster=%s with user=%s: %s", d.cluster, d.user, err)
	}

	// read ceph.conf and setup connection
	if d.config == "" {
		err = cephConn.ReadDefaultConfigFile()
	} else {
		err = cephConn.ReadConfigFile(d.config)
	}
	if err != nil {
		log.Panicf("ERROR: Unable to read ceph config: %s", err)
	}

	err = cephConn.Connect()
	if err != nil {
		log.Panicf("ERROR: Unable to connect to Ceph: %s", err)
	}

	// can now set conn in driver
	d.conn = cephConn

	// setup the default context (pool most likely to be used)
	defaultContext, err := d.openContext(d.defaultPool)
	if err != nil {
		log.Panicf("ERROR: Unable to connect to default Ceph Pool: %s", err)
	}
	d.defaultIoctx = defaultContext
}

// shutdownContext will destroy any non-default ioctx
func (d *cephRBDVolumeDriver) shutdownContext(ioctx *rados.IOContext) {
	if ioctx != nil && ioctx != d.defaultIoctx {
		ioctx.Destroy()
	}
}

// openContext provides access to a specific Ceph Pool
func (d *cephRBDVolumeDriver) openContext(pool string) (*rados.IOContext, error) {
	// check default first
	if pool == d.defaultPool && d.defaultIoctx != nil {
		return d.defaultIoctx, nil
	}
	// otherwise open new pool context ... call shutdownContext(ctx) to destroy
	ioctx, err := d.conn.OpenIOContext(pool)
	if err != nil {
		// TODO: make sure we aren't hiding a useful error struct by casting to string?
		msg := fmt.Sprintf("Unable to open context(%s): %s", pool, err)
		log.Printf("ERROR: " + msg)
		return ioctx, errors.New(msg)
	}
	return ioctx, nil
}

// mountpoint returns the expected path on host
func (d *cephRBDVolumeDriver) mountpoint(pool, name string) string {
	return filepath.Join(d.root, pool, name)
}

// parseImagePoolNameSize parses out any optional parameters from Image Name
// passed from docker run. Fills in unspecified options with default pool or
// size.
//
// Returns: pool, image-name, size, error
//
func (d *cephRBDVolumeDriver) parseImagePoolNameSize(fullname string) (string, string, int, error) {
	// Examples of regexp matches:
	//   foo: ["foo" "" "" "foo" "" ""]
	//   foo@1024: ["foo@1024" "" "" "foo" "@1024" "1024"]
	//   pool/foo: ["pool/foo" "pool/" "pool" "foo" "" ""]
	//   pool/foo@1024: ["pool/foo@1024" "pool/" "pool" "foo" "@1024" "1024"]
	//
	// Match indices:
	//   0: matched string
	//   1: pool with slash
	//   2: pool no slash
	//   3: image name
	//   4: size with @
	//   5: size only
	//
	matches := imageNameRegexp.FindStringSubmatch(fullname)
	if isDebugEnabled() {
		log.Printf("DEBUG: parseImagePoolNameSize: \"%s\": %q", fullname, matches)
	}
	if len(matches) != 6 {
		return "", "", 0, errors.New("Unable to parse image name: " + fullname)
	}

	// 2: pool
	pool := d.defaultPool // defaul pool for plugin
	if matches[2] != "" {
		pool = matches[2]
	}

	// 3: image
	imagename := matches[3]

	// 5: size
	size := *defaultImageSizeMB
	if matches[5] != "" {
		var err error
		size, err = strconv.Atoi(matches[5])
		if err != nil {
			log.Printf("WARN: using default. unable to parse int from %s: %s", matches[5], err)
			size = *defaultImageSizeMB
		}
	}

	return pool, imagename, size, nil
}

// rbdImageExists will check for an existing Ceph RBD Image
func (d *cephRBDVolumeDriver) rbdImageExists(pool, findName string) (bool, error) {
	log.Printf("INFO: checking if rbdImageExists(%s/%s)", pool, findName)
	if findName != "" {
		ctx, err := d.openContext(pool)
		if err != nil {
			return false, err
		}
		defer d.shutdownContext(ctx)

		rbdImageNames, err := rbd.GetImageNames(ctx)
		if err != nil {
			log.Printf("ERROR: Unable to get Ceph RBD Image list: %s", err)
			return false, err
		}
		for _, imageName := range rbdImageNames {
			if imageName == findName {
				return true, nil
			}
		}
	}
	return false, nil
}

// createRBDImage will create a new Ceph block device and make a filesystem on it
func (d *cephRBDVolumeDriver) createRBDImage(pool string, name string, size int, fstype string) error {
	log.Printf("INFO: Attempting to create new RBD Image: (%s/%s, %s, %s)", pool, name, size, fstype)

	// check that fs is valid type (needs mkfs.fstype in PATH)
	mkfs, err := exec.LookPath("mkfs." + fstype)
	if err != nil {
		msg := fmt.Sprintf("Unable to find mkfs for %s in PATH: %s", fstype, err)
		return errors.New(msg)
	}

	// create the block device image with format=2
	//  should we enable all v2 image features?: +1: layering support +2: striping v2 support +4: exclusive locking support +8: object map support
	// NOTE: i tried but "2015-08-02 20:24:36.726758 7f87787907e0 -1 librbd: librbd does not support requested features."
	// NOTE: I also tried just image-features=4 (locking) - but map will fail:
	//       sudo rbd unmap mynewvol =>  rbd: 'mynewvol' is not a block device, rbd: unmap failed: (22) Invalid argument
	//	"--image-features", strconv.Itoa(4),
	_, err = sh(
		"rbd", "create",
		"--id", d.user,
		"--pool", pool,
		"--image-format", strconv.Itoa(2),
		"--size", strconv.Itoa(size),
		name,
	)
	if err != nil {
		return err
	}

	// lock it temporarily for fs creation
	lockname, err := d.lockImage(pool, name)
	if err != nil {
		// TODO: defer image delete?
		return err
	}

	// map to kernel device
	device, err := d.mapImage(pool, name)
	if err != nil {
		defer d.unlockImage(pool, name, lockname)
		return err
	}

	// make the filesystem
	_, err = sh(mkfs, device)
	if err != nil {
		defer d.unmapImageDevice(device)
		defer d.unlockImage(pool, name, lockname)
		return err
	}

	// TODO: should we now just defer both of unmap and unlock? or catch err?
	//
	// unmap
	err = d.unmapImageDevice(device)
	if err != nil {
		// ? if we cant unmap -- are we screwed? should we unlock?
		return err
	}

	// unlock
	err = d.unlockImage(pool, name, lockname)
	if err != nil {
		return err
	}

	return nil
}

// FIXME: getting panics when trying to run below go-ceph code, at ListLockers():
//
// see https://github.com/yp-engineering/rbd-docker-plugin/issues/3
//
// rbdImageIsLocked returns true if named image is already locked
func (d *cephRBDVolumeDriver) rbdImageIsLocked(pool, name string) (bool, error) {
	if pool == "" || name == "" {
		return true, errors.New("rbdImageIsLocked: pool and name required")
	}

	// connext to pool
	ctx, err := d.openContext(pool)
	if err != nil {
		return true, err
	}
	defer d.shutdownContext(ctx)

	// make the image struct
	rbdImage := rbd.GetImage(ctx, name)

	// open it (read-only)
	err = rbdImage.Open(true)
	if err != nil {
		log.Printf("ERROR: opening rbd image(%s): %s", name, err)
		return true, err
	}
	defer rbdImage.Close()

	// check for locks -- for our purposes, with even one lock we consider image locked
	//lockers := []rbd.Locker{}
	//lockers := make([]rbd.Locker, 10)
	tag, lockers, err := rbdImage.ListLockers()
	if err != nil {
		log.Printf("ERROR: retrieving Lockers list for Image(%s): %s", name, err)
		return true, err
	}
	if len(lockers) > 0 {
		log.Printf("WARN: RBD Image is locked: tag=%s, lockers=%q", tag, lockers)
		return true, nil
	}

	return false, nil
}

// lockImage locks image and returns locker cookie name
func (d *cephRBDVolumeDriver) lockImage(pool, imagename string) (string, error) {
	log.Printf("INFO: lockImage(%s/%s)", pool, imagename)

	ctx, err := d.openContext(pool)
	if err != nil {
		return "", err
	}
	defer d.shutdownContext(ctx)

	// build image struct
	rbdImage := rbd.GetImage(ctx, imagename)

	// open it (read-only)
	err = rbdImage.Open(true)
	if err != nil {
		log.Printf("ERROR: opening rbd image(%s): %s", imagename, err)
		return "", err
	}
	defer rbdImage.Close()

	// lock it using hostname
	locker := d.localLockerCookie()
	err = rbdImage.LockExclusive(locker)
	if err != nil {
		return locker, err
	}
	return locker, nil
}

// localLockerCookie returns the Hostname
func (d *cephRBDVolumeDriver) localLockerCookie() string {
	host, err := os.Hostname()
	if err != nil {
		log.Printf("WARN: HOST_UNKNOWN: unable to get hostname: %s", err)
		host = "HOST_UNKNOWN"
	}
	return host
}

// unlockImage releases the exclusive lock on an image
func (d *cephRBDVolumeDriver) unlockImage(pool, imagename, locker string) error {
	if locker == "" {
		log.Printf("WARN: Attempting to unlock image(%s/%s) for empty locker using default hostname", pool, imagename)
		// try to unlock using the local hostname
		locker = d.localLockerCookie()
	}
	log.Printf("INFO: unlockImage(%s/%s, %s)", pool, imagename, locker)

	ctx, err := d.openContext(pool)
	if err != nil {
		return err
	}
	defer d.shutdownContext(ctx)

	// build image struct
	rbdImage := rbd.GetImage(ctx, imagename)

	// open it (read-only)
	err = rbdImage.Open(true)
	if err != nil {
		log.Printf("ERROR: opening rbd image(%s): %s", imagename, err)
		return err
	}
	defer rbdImage.Close()
	return rbdImage.Unlock(locker)
}

// removeRBDImage will remove a Ceph RBD image - no undo available
func (d *cephRBDVolumeDriver) removeRBDImage(pool, name string) error {
	log.Println("INFO: Remove RBD Image(%s/%s)", pool, name)

	ctx, err := d.openContext(pool)
	if err != nil {
		return err
	}
	defer d.shutdownContext(ctx)

	// build image struct
	rbdImage := rbd.GetImage(ctx, name)

	// remove the block device image
	return rbdImage.Remove()
}

// renameRBDImage will move a Ceph RBD image to new name
func (d *cephRBDVolumeDriver) renameRBDImage(pool, name, newname string) error {
	log.Println("INFO: Rename RBD Image(%s/%s -> %s)", pool, name, newname)

	ctx, err := d.openContext(pool)
	if err != nil {
		return err
	}
	defer d.shutdownContext(ctx)

	// build image struct
	rbdImage := rbd.GetImage(ctx, name)

	// remove the block device image
	return rbdImage.Rename(newname)
}

//
// NOTE: the following are Shell commands for low level RBD or Device operations
//

// RBD subcommands

// mapImage will map the RBD Image to a kernel device
func (d *cephRBDVolumeDriver) mapImage(pool, imagename string) (string, error) {
	return sh("rbd", "map", "--id", d.user, "--pool", pool, imagename)
}

// unmapImageDevice will release the mapped kernel device
// TODO: does this operation even require a user --id ? I can unmap a device without or with a different id and rbd doesn't seem to care
func (d *cephRBDVolumeDriver) unmapImageDevice(device string) error {
	_, err := sh("rbd", "unmap", device)
	return err
}

// DEPRECATED sh_removeRBDImage will remove a Ceph RBD image - no undo available
func (d *cephRBDVolumeDriver) sh_removeRBDImage(pool, name string) error {
	log.Println("INFO: Remove RBD Image(%s/%s)", pool, name)

	// remove the block device image
	_, err := sh(
		"rbd", "rm",
		"--id", d.user,
		"--pool", pool,
		name,
	)
	if err != nil {
		return err
	}
	return nil
}

// DEPRECATED sh_renameRBDImage will move a Ceph RBD image to new name
func (d *cephRBDVolumeDriver) sh_renameRBDImage(pool, name, newname string) error {
	log.Println("INFO: Rename RBD Image(%s/%s -> %s)", pool, name, newname)

	_, err := sh(
		"rbd", "rename",
		"--id", d.user,
		"--pool", pool,
		name,
		newname,
	)
	if err != nil {
		return err
	}
	return nil
}

// Callouts to other shell commands: blkid, mount, umount

// deviceType identifies Image FS Type - requires RBD image to be mapped to kernel device
func (d *cephRBDVolumeDriver) deviceType(device string) (string, error) {
	// blkid Output:
	//	/dev/rbd3: xfs
	blkid, err := sh("blkid", "-o", "value", "-s", "TYPE", device)
	if err != nil {
		return "", err
	}
	if blkid != "" {
		return blkid, nil
	} else {
		return "", errors.New("Unable to determine device fs type from blkid")
	}
}

// mountDevice will call mount on kernel device with a docker volume subdirectory
func (d *cephRBDVolumeDriver) mountDevice(device, mountdir, fstype string) error {
	_, err := sh("mount", "-t", fstype, device, mountdir)
	return err
}

// unmountDevice will call umount on kernel device to unmount from host's docker subdirectory
func (d *cephRBDVolumeDriver) unmountDevice(device string) error {
	_, err := sh("umount", device)
	return err
}

// UTIL

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

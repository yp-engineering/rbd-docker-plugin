// Copyright 2015 YP LLC.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.
package main

// Ceph RBD Docker VolumeDriver Plugin
//
// Run rbd-docker-plugin service, which creates a socket that can accept JSON
// HTTP POSTs from docker engine.
//
// Due to some issues using the go-ceph library for locking/unlocking, we
// reimplemented all functionality to use shell CLI commands via the 'rbd'
// executable.  To re-enable old go-ceph functionality, use --go-ceph flag.
//
// System Requirements:
//   - requires rbd CLI binary for shell operation (default)
//   - requires ceph, rados and rbd development headers to use go-ceph
//     yum install ceph-devel librados2-devel librbd1-devel
//
// Plugin name: rbd  (yp-rbd? ceph-rbd?) -- now configurable via --name
//
// 	docker run --volume-driver=rbd -v imagename:/mnt/dir IMAGE [CMD]
//
// golang github code examples:
// - https://github.com/docker/docker/blob/master/experimental/plugins_volume.md
// - https://github.com/ceph/go-ceph
// - https://github.com/docker/go-plugins-helpers/tree/master/volume
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
	"time"

	"github.com/ceph/go-ceph/rados"
	"github.com/ceph/go-ceph/rbd"
	dkvolume "github.com/docker/go-plugins-helpers/volume"
)

// TODO: use versioned dependencies -- e.g. newest dkvolume already has breaking changes?

var (
	imageNameRegexp    = regexp.MustCompile(`^(([-_.[:alnum:]]+)/)?([-_.[:alnum:]]+)(@([0-9]+))?$`) // optional pool or size in image name
	rbdUnmapBusyRegexp = regexp.MustCompile(`^exit status 16$`)
)

// Volume is the Docker concept which we map onto a Ceph RBD Image
type Volume struct {
	name   string // RBD Image name
	device string // local host kernel device (e.g. /dev/rbd1)
	locker string // track the lock name
	fstype string
	pool   string
}

// TODO: finish modularizing and split out go-ceph and shell-cli implementations
//
// in progress: interface to abstract the ceph operations - either go-ceph lib or sh cli commands
type RbdImageDriver interface {
	// shutdown()
	// connect(pool string) error // ?? only go-ceph

	rbdImageExists(pool, findName string) (bool, error)
	createRBDImage(pool string, name string, size int, fstype string) error
	rbdImageIsLocked(pool, name string) (bool, error)
	lockImage(pool, imagename string) (string, error)
	unlockImage(pool, imagename, locker string) error
	removeRBDImage(pool, name string) error
	renameRBDImage(pool, name, newname string) error
	// mapImage(pool, name string)
	// unmapImageDevice(device string)
	// mountDevice(device, mount, fstype string)
	// unmountDevice(device string)
}

//

// our driver type for impl func
type cephRBDVolumeDriver struct {
	// - using default ceph cluster name ("ceph")
	// - using default ceph config (/etc/ceph/<cluster>.conf)
	//
	// TODO: when starting, what if there are mounts already for RBD devices?
	// do we ingest them as our own or ... currently fails if locked
	//
	// TODO: use a chan as semaphore instead of mutex in driver?

	name    string             // unique name for plugin
	cluster string             // ceph cluster to use (default: ceph)
	user    string             // ceph user to use (default: admin)
	pool    string             // ceph pool to use (default: rbd)
	root    string             // scratch dir for mounts for this plugin
	config  string             // ceph config file to read
	volumes map[string]*Volume // track locally mounted volumes
	m       *sync.Mutex        // mutex to guard operations that change volume maps or use conn

	useGoCeph bool             // whether to setup/use go-ceph lib methods (default: false - use shell cli)
	conn      *rados.Conn      // create a connection for each API operation
	ioctx     *rados.IOContext // context for requested pool
}

// newCephRBDVolumeDriver builds the driver struct, reads config file and connects to cluster
func newCephRBDVolumeDriver(pluginName, cluster, userName, defaultPoolName, rootBase, config string, useGoCeph bool) cephRBDVolumeDriver {
	// the root mount dir will be based on docker default root and plugin name - pool added later per volume
	mountDir := filepath.Join(rootBase, pluginName)
	log.Printf("INFO: newCephRBDVolumeDriver: setting base mount dir=%s", mountDir)

	// fill everything except the connection and context
	driver := cephRBDVolumeDriver{
		name:      pluginName,
		cluster:   cluster,
		user:      userName,
		pool:      defaultPoolName,
		root:      mountDir,
		config:    config,
		volumes:   map[string]*Volume{},
		m:         &sync.Mutex{},
		useGoCeph: useGoCeph,
	}

	return driver
}

// Capabilities
// Scope: global - images managed using this plugin can be considered "global"
// TODO: make configurable
func (d cephRBDVolumeDriver) Capabilities(r dkvolume.Request) dkvolume.Response {
	return dkvolume.Response{
		Capabilities: dkvolume.Capability{
			Scope: "global",
		},
	}
}

// ************************************************************
//
// Implement the Docker VolumeDriver API via dkvolume interface
//
// Using https://github.com/docker/go-plugins-helpers/tree/master/volume
//
// ************************************************************

// Create will ensure the RBD image requested is available.  Plugin requires
// --create option to provision new RBD images.
//
// Docker Volume Create Options:
//   size   - in MB
//   pool
//   fstype
//
//
// POST /VolumeDriver.Create
//
// Request:
//    {
//      "Name": "volume_name",
//      "Opts": {}
//    }
//    Instruct the plugin that the user wants to create a volume, given a user
//    specified volume name. The plugin does not need to actually manifest the
//    volume on the filesystem yet (until Mount is called).
//
// Response:
//    { "Err": null }
//    Respond with a string error if an error occurred.
//
func (d cephRBDVolumeDriver) Create(r dkvolume.Request) dkvolume.Response {
	log.Printf("INFO: API Create(%q)", r)
	d.m.Lock()
	defer d.m.Unlock()

	return d.createImage(r)
}

func (d cephRBDVolumeDriver) createImage(r dkvolume.Request) dkvolume.Response {
	log.Printf("INFO: createImage(%q)", r)

	fstype := *defaultImageFSType

	// parse image name optional/default pieces
	pool, name, size, err := d.parseImagePoolNameSize(r.Name)
	if err != nil {
		log.Printf("ERROR: parsing volume: %s", err)
		return dkvolume.Response{Err: err.Error()}
	}

	// Options to override from `docker volume create -o OPT=VAL ...`
	if r.Options["pool"] != "" {
		pool = r.Options["pool"]
	}
	if r.Options["size"] != "" {
		size, err = strconv.Atoi(r.Options["size"])
		if err != nil {
			log.Printf("WARN: using default size. unable to parse int from %s: %s", r.Options["size"], err)
			size = *defaultImageSizeMB
		}
	}
	if r.Options["fstype"] != "" {
		fstype = r.Options["fstype"]
	}

	// check for mount
	mount := d.mountpoint(pool, name)

	// do we already know about this volume? return early
	if _, found := d.volumes[mount]; found {
		log.Println("INFO: Volume is already in known mounts: " + mount)
		return dkvolume.Response{}
	}

	// otherwise, connect to Ceph and check ceph rbd api for it
	if d.useGoCeph {
		err = d.connect(pool)
		if err != nil {
			log.Printf("ERROR: unable to connect to ceph and access pool: %s", err)
			return dkvolume.Response{Err: err.Error()}
		}
		defer d.shutdown()
	}

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
		err = d.createRBDImage(pool, name, size, fstype)
		if err != nil {
			errString := fmt.Sprintf("Unable to create Ceph RBD Image(%s): %s", name, err)
			log.Println("ERROR: " + errString)
			return dkvolume.Response{Err: errString}
		}
	}

	return dkvolume.Response{}
}

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
	log.Printf("INFO: API Remove(%s)", r)
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

	// connect to Ceph and check ceph rbd api for it
	if d.useGoCeph {
		err = d.connect(pool)
		if err != nil {
			log.Printf("ERROR: unable to connect to ceph and access pool: %s", err)
			return dkvolume.Response{Err: err.Error()}
		}
		defer d.shutdown()
	}

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

	// attempt to gain lock before remove - lock seems to disappear after rm (but not after rename)
	locker, err := d.lockImage(pool, name)
	if err != nil {
		errString := fmt.Sprintf("Unable to lock image for remove: %s", name)
		log.Println("ERROR: " + errString)
		return dkvolume.Response{Err: errString}
	}

	// remove action can be: ignore, delete or rename
	if removeActionFlag == "delete" {
		// delete it (for real - destroy it ... )
		err = d.removeRBDImage(pool, name)
		if err != nil {
			errString := fmt.Sprintf("Unable to remove Ceph RBD Image(%s): %s", name, err)
			log.Println("ERROR: " + errString)
			defer d.unlockImage(pool, name, locker)
			return dkvolume.Response{Err: errString}
		}
		defer d.unlockImage(pool, name, locker)
	} else if removeActionFlag == "rename" {
		// just rename it (in case needed later, or can be culled via script)
		err = d.renameRBDImage(pool, name, "zz_"+name)
		if err != nil {
			errString := fmt.Sprintf("Unable to rename with zz_ prefix: RBD Image(%s): %s", name, err)
			log.Println("ERROR: " + errString)
			// unlock by old name
			defer d.unlockImage(pool, name, locker)
			return dkvolume.Response{Err: errString}
		}
		// unlock by new name
		defer d.unlockImage(pool, "zz_"+name, locker)
	} else {
		// ignore the remove call - but unlock ?
		defer d.unlockImage(pool, name, locker)
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
// TODO: utilize the new MountRequest.ID field to track volumes
func (d cephRBDVolumeDriver) Mount(r dkvolume.MountRequest) dkvolume.Response {
	log.Printf("INFO: API Mount(%s)", r)
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
	// check that the image is not locked already
	//locked, err := d.rbdImageIsLocked(name)
	//if locked || err != nil {
	//	log.Printf("ERROR: checking for RBD Image(%s) lock: %s", name, err)
	//	return dkvolume.Response{Err: "RBD Image locked"}
	//}

	// attempt to lock
	locker, err := d.lockImage(pool, name)
	if err != nil {
		log.Printf("ERROR: locking RBD Image(%s): %s", name, err)
		return dkvolume.Response{Err: "Unable to get Exclusive Lock"}
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

	// double check image filesystem if possible
	err = d.verifyDeviceFilesystem(device, fstype)
	if err != nil {
		log.Printf("ERROR: filesystem may need repairs: %s", err)
		// failsafe: need to release lock and unmap kernel device
		defer d.unmapImageDevice(device)
		defer d.unlockImage(pool, name, locker)
		return dkvolume.Response{Err: "Image filesystem has errors, requires manual repairs"}
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
	d.volumes[mount] = &Volume{
		name:   name,
		device: device,
		locker: locker,
		fstype: fstype,
		pool:   pool,
	}

	return dkvolume.Response{Mountpoint: mount}
}

// Get the list of volumes registered with the plugin.
//
// POST /VolumeDriver.List
//
// Request:
//    {}
//    List the volumes mapped by this plugin.
//
// Response:
//    { "Volumes": [ { "Name": "volume_name", "Mountpoint": "/path/to/directory/on/host" } ], "Err": null }
//    Respond with an array containing pairs of known volume names and their
//    respective paths on the host filesystem (where the volumes have been
//    made available).
//
func (d cephRBDVolumeDriver) List(r dkvolume.Request) dkvolume.Response {
	vols := make([]*dkvolume.Volume, 0, len(d.volumes))
	// for each registered mountpoint
	for k, v := range d.volumes {
		// append it and its name to the result
		vols = append(vols, &dkvolume.Volume{
			Name:       v.name,
			Mountpoint: k,
		})
	}

	log.Printf("INFO: List request => %s", vols)
	return dkvolume.Response{Volumes: vols}
}

// Get the volume info.
//
// POST /VolumeDriver.Get
//
// Request:
//    { "Name": "volume_name" }
//    Docker needs reminding of the path to the volume on the host.
//
// Response:
//    { "Volume": { "Name": "volume_name", "Mountpoint": "/path/to/directory/on/host" }, "Err": null }
//    Respond with a tuple containing the name of the queried volume and the
//    path on the host filesystem where the volume has been made available,
//    and/or a string error if an error occurred.
//
func (d cephRBDVolumeDriver) Get(r dkvolume.Request) dkvolume.Response {
	// parse full image name for optional/default pieces
	pool, name, _, err := d.parseImagePoolNameSize(r.Name)
	if err != nil {
		log.Printf("ERROR: parsing volume: %s", err)
		return dkvolume.Response{Err: err.Error()}
	}

	// Check to see if the image exists
	exists, err := d.rbdImageExists(pool, name)
	if err != nil {
		log.Printf("WARN: checking for RBD Image: %s", err)
		return dkvolume.Response{Err: err.Error()}
	}
	mountPath := d.mountpoint(pool, name)
	if !exists {
		log.Printf("WARN: Image %s does not exist", r.Name)
		delete(d.volumes, mountPath)
		return dkvolume.Response{Err: fmt.Sprintf("Image %s does not exist", r.Name)}
	}
	log.Printf("INFO: Get request(%s) => %s", name, mountPath)

	// TODO: what to do if the mountpoint registry (d.volumes) has a different name?

	return dkvolume.Response{Volume: &dkvolume.Volume{Name: r.Name, Mountpoint: mountPath}}
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
// NOTE: this method does not require the Ceph connection
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
// FIXME: TODO: we are getting an Unmount call from docker daemon after a
// failed Mount (e.g. device locked), which causes the device to be
// unmounted/unmapped/unlocked while possibly in use by another container --
// revisit the API, are we doing something wrong or perhaps we can fail sooner
//
// TODO: utilize the new UnmountRequest.ID field to track volumes
func (d cephRBDVolumeDriver) Unmount(r dkvolume.UnmountRequest) dkvolume.Response {
	log.Printf("INFO: API Unmount(%s)", r)
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

	// connect to Ceph so we can manipulate RBD Image
	if d.useGoCeph {
		err = d.connect(pool)
		if err != nil {
			log.Printf("ERROR: unable to connect to ceph and access pool: %s", err)
			return dkvolume.Response{Err: err.Error()}
		}
		defer d.shutdown()
	}

	// check if it's in our mounts - we may not know about it if plugin was started late?
	vol, found := d.volumes[mount]
	if !found {
		log.Printf("WARN: Volume is not in known mounts: will attempt limited Unmount: %s/%s", pool, name)
		// set up a fake Volume with defaults ...
		// - device is /dev/rbd/<pool>/<image> in newer ceph versions
		// - assume we are the locker (will fail if locked from another host)
		vol = &Volume{
			pool:   pool,
			name:   name,
			device: fmt.Sprintf("/dev/rbd/%s/%s", pool, name),
			locker: d.localLockerCookie(),
		}
	}

	// unmount
	// NOTE: this might succeed even if device is still in use inside container. device will dissappear from host side but still be usable inside container :(
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
		// NOTE: rbd unmap exits 16 if device is still being used - unlike umount.  try to recover differently in that case
		if rbdUnmapBusyRegexp.MatchString(err.Error()) {
			// can't always re-mount and not sure if we should here ... will be cleaned up once original container goes away
			log.Printf("WARN: unmap failed due to busy device, early exit from this Unmount request.")
			return dkvolume.Response{Err: err.Error()}
		}
		// other error, failsafe: proceed to attempt to unlock
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

// shutdown and connect are used when d.useGoCeph == true

// shutdown closes the connection - maybe not needed unless we recreate conn?
// more info:
// - https://github.com/ceph/go-ceph/blob/f251b53/rados/ioctx.go#L140
// - http://ceph.com/docs/master/rados/api/librados/
func (d *cephRBDVolumeDriver) shutdown() {
	log.Println("INFO: Ceph RBD Driver shutdown() called")
	if d.ioctx != nil {
		d.ioctx.Destroy()
	}
	if d.conn != nil {
		d.conn.Shutdown()
	}
}

// connect builds up the ceph conn and default pool
func (d *cephRBDVolumeDriver) connect(pool string) error {
	log.Printf("INFO: connect() to Ceph via go-ceph, with pool: %s", pool)

	// create the go-ceph Client Connection
	var cephConn *rados.Conn
	var err error
	if d.cluster == "" {
		cephConn, err = rados.NewConnWithUser(d.user)
	} else {
		// FIXME: TODO: can't seem to use a cluster name -- get error -22 from noahdesu/go-ceph/rados:
		// panic: Unable to create ceph connection to cluster=ceph with user=admin: rados: ret=-22
		cephConn, err = rados.NewConnWithClusterAndUser(d.cluster, d.user)
	}
	if err != nil {
		log.Printf("ERROR: Unable to create ceph connection to cluster=%s with user=%s: %s", d.cluster, d.user, err)
		return err
	}

	// read ceph.conf and setup connection
	if d.config == "" {
		err = cephConn.ReadDefaultConfigFile()
	} else {
		err = cephConn.ReadConfigFile(d.config)
	}
	if err != nil {
		log.Printf("ERROR: Unable to read ceph config: %s", err)
		return err
	}

	err = cephConn.Connect()
	if err != nil {
		log.Printf("ERROR: Unable to connect to Ceph: %s", err)
		return err
	}

	// can now set conn in driver
	d.conn = cephConn

	// setup the requested pool context
	ioctx, err := d.goceph_openContext(pool)
	if err != nil {
		return err
	}
	d.ioctx = ioctx

	return nil
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
func (d *cephRBDVolumeDriver) parseImagePoolNameSize(fullname string) (pool string, imagename string, size int, err error) {
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
	pool = d.pool // defaul pool for plugin
	if matches[2] != "" {
		pool = matches[2]
	}

	// 3: image
	imagename = matches[3]

	// 5: size
	size = *defaultImageSizeMB
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
	if d.useGoCeph {
		return d.goceph_rbdImageExists(pool, findName)
	}
	return d.sh_rbdImageExists(pool, findName)
}

// sh_rbdImageExists uses rbd info to check for ceph rbd image
func (d *cephRBDVolumeDriver) sh_rbdImageExists(pool, findName string) (bool, error) {
	_, err := d.rbdsh(pool, "info", findName)
	if err != nil {
		// NOTE: even though method signature returns err - we take the error
		// in this instance as the indication that the image does not exist
		// TODO: can we double check exit value for exit status 2 ?
		return false, nil
	}
	return true, nil
}

func (d *cephRBDVolumeDriver) goceph_rbdImageExists(pool, findName string) (bool, error) {
	log.Printf("INFO: checking if rbdImageExists(%s/%s)", pool, findName)
	if findName == "" {
		return false, fmt.Errorf("Empty Ceph RBD Image name")
	}

	ctx, err := d.goceph_openContext(pool)
	if err != nil {
		return false, err
	}
	defer d.goceph_shutdownContext(ctx)

	img := rbd.GetImage(ctx, findName)
	err = img.Open(true)
	defer img.Close()
	if err != nil {
		if err == rbd.RbdErrorNotFound {
			log.Printf("INFO: Ceph RBD Image ('%s') not found: %s", findName, err)
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// goceph_shutdownContext will destroy any non-default ioctx
func (d *cephRBDVolumeDriver) goceph_shutdownContext(ioctx *rados.IOContext) {
	if ioctx != nil {
		ioctx.Destroy()
	}
}

// goceph_openContext provides access to a specific Ceph Pool
func (d *cephRBDVolumeDriver) goceph_openContext(pool string) (*rados.IOContext, error) {
	// setup the requested pool context
	ioctx, err := d.conn.OpenIOContext(pool)
	if err != nil {
		// TODO: make sure we aren't hiding a useful error struct by casting to string?
		msg := fmt.Sprintf("Unable to open context(%s): %s", pool, err)
		log.Printf("ERROR: " + msg)
		return ioctx, errors.New(msg)
	}
	return ioctx, nil
}

// createRBDImage will create a new Ceph block device and make a filesystem on it
func (d *cephRBDVolumeDriver) createRBDImage(pool string, name string, size int, fstype string) error {
	// NOTE: there is no goceph_ version of this func - but parts of sh version do (lock/unlock)
	return d.sh_createRBDImage(pool, name, size, fstype)
}

func (d *cephRBDVolumeDriver) sh_createRBDImage(pool string, name string, size int, fstype string) error {
	log.Printf("INFO: Attempting to create new RBD Image: (%s/%s, %s, %s)", pool, name, size, fstype)

	// check that fs is valid type (needs mkfs.fstype in PATH)
	mkfs, err := exec.LookPath("mkfs." + fstype)
	if err != nil {
		msg := fmt.Sprintf("Unable to find mkfs for %s in PATH: %s", fstype, err)
		return errors.New(msg)
	}

	// TODO: create a go-ceph Create(..) func for this?

	// create the block device image with format=2 (v2)
	//  should we enable all v2 image features?: +1: layering support +2: striping v2 support +4: exclusive locking support +8: object map support
	// NOTE: i tried but "2015-08-02 20:24:36.726758 7f87787907e0 -1 librbd: librbd does not support requested features."
	// NOTE: I also tried just image-features=4 (locking) - but map will fail:
	//       sudo rbd unmap mynewvol =>  rbd: 'mynewvol' is not a block device, rbd: unmap failed: (22) Invalid argument
	//	"--image-features", strconv.Itoa(4),
	_, err = d.rbdsh(
		pool, "create",
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

	// make the filesystem - give it some time
	_, err = shWithTimeout(5*time.Minute, mkfs, device)
	if err != nil {
		defer d.unmapImageDevice(device)
		defer d.unlockImage(pool, name, lockname)
		return err
	}

	// TODO: should we chown/chmod the directory? e.g. non-root container users won't be able to write

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

// rbdImageIsLocked returns true if named image is already locked
func (d *cephRBDVolumeDriver) rbdImageIsLocked(pool, name string) (bool, error) {
	if d.useGoCeph {
		return d.goceph_rbdImageIsLocked(pool, name)
	}
	return d.sh_rbdImageIsLocked(pool, name)
}

func (d *cephRBDVolumeDriver) sh_rbdImageIsLocked(pool, name string) (bool, error) {
	// check the output for a lock -- if blank or error, assume not locked (?)
	out, err := d.rbdsh(pool, "lock", "ls", name)
	if err != nil || out != "" {
		return false, err
	}
	// otherwise - no error and output is not blank - assume a lock exists ...
	return true, nil
}

// FIXME: getting panics when trying to run below go-ceph code, at ListLockers():
//
// see https://github.com/yp-engineering/rbd-docker-plugin/issues/3
//
func (d *cephRBDVolumeDriver) goceph_rbdImageIsLocked(pool, name string) (bool, error) {
	if pool == "" || name == "" {
		return true, errors.New("rbdImageIsLocked: pool and name required")
	}

	// make the image struct
	rbdImage := rbd.GetImage(d.ioctx, name)

	// open it (read-only)
	err := rbdImage.Open(true)
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
	if d.useGoCeph {
		return d.goceph_lockImage(pool, imagename)
	}
	return d.sh_lockImage(pool, imagename)
}

func (d *cephRBDVolumeDriver) sh_lockImage(pool, imagename string) (string, error) {
	cookie := d.localLockerCookie()
	_, err := d.rbdsh(pool, "lock", "add", imagename, cookie)
	if err != nil {
		return "", err
	}
	return cookie, nil
}

func (d *cephRBDVolumeDriver) goceph_lockImage(pool, imagename string) (string, error) {
	log.Printf("INFO: lockImage(%s/%s)", pool, imagename)

	// build image struct
	rbdImage := rbd.GetImage(d.ioctx, imagename)

	// open it (read-only)
	err := rbdImage.Open(true)
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

	if d.useGoCeph {
		return d.goceph_unlockImage(pool, imagename, locker)
	}
	return d.sh_unlockImage(pool, imagename, locker)
}

func (d *cephRBDVolumeDriver) sh_unlockImage(pool, imagename, locker string) error {
	// first - we need to discover the client id of the locker -- so we have to
	// `rbd lock list` and grep out fields
	out, err := d.rbdsh(pool, "lock", "list", imagename)
	if err != nil || out == "" {
		log.Printf("ERROR: image not locked or ceph rbd error: %s", err)
		return err
	}

	// parse out client id -- assume we looking for a line with the locker cookie on it --
	var clientid string
	lines := grepLines(out, locker)
	if isDebugEnabled() {
		log.Printf("DEBUG: found lines matching %s:\n%s\n", locker, lines)
	}
	if len(lines) == 1 {
		// grab first word of first line as the client.id ?
		tokens := strings.SplitN(lines[0], " ", 2)
		if tokens[0] != "" {
			clientid = tokens[0]
		}
	}

	if clientid == "" {
		return errors.New("sh_unlockImage: Unable to determine client.id")
	}

	_, err = d.rbdsh(pool, "lock", "rm", imagename, locker, clientid)
	if err != nil {
		return err
	}
	return nil
}

func (d *cephRBDVolumeDriver) goceph_unlockImage(pool, imagename, locker string) error {
	// build image struct
	rbdImage := rbd.GetImage(d.ioctx, imagename)

	// open it (read-only)
	//err := rbdImage.Open(true)
	err := rbdImage.Open()
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

	if d.useGoCeph {
		return d.goceph_removeRBDImage(pool, name)
	}
	return d.sh_removeRBDImage(pool, name)
}

// sh_removeRBDImage will remove a Ceph RBD image - no undo available
func (d *cephRBDVolumeDriver) sh_removeRBDImage(pool, name string) error {
	// remove the block device image
	_, err := d.rbdsh(pool, "rm", name)

	if err != nil {
		return err
	}
	return nil
}

func (d *cephRBDVolumeDriver) goceph_removeRBDImage(pool, name string) error {
	// build image struct
	rbdImage := rbd.GetImage(d.ioctx, name)

	// remove the block device image
	return rbdImage.Remove()
}

// renameRBDImage will move a Ceph RBD image to new name
func (d *cephRBDVolumeDriver) renameRBDImage(pool, name, newname string) error {
	log.Println("INFO: Rename RBD Image(%s/%s -> %s)", pool, name, newname)

	if d.useGoCeph {
		return d.goceph_renameRBDImage(pool, name, newname)
	}
	return d.sh_renameRBDImage(pool, name, newname)
}

// sh_renameRBDImage will move a Ceph RBD image to new name
func (d *cephRBDVolumeDriver) sh_renameRBDImage(pool, name, newname string) error {
	log.Println("INFO: Rename RBD Image(%s/%s -> %s)", pool, name, newname)

	out, err := d.rbdsh(pool, "rename", name, newname)
	if err != nil {
		log.Printf("ERROR: unable to rename: %s: %s", err, out)
		return err
	}
	return nil
}

func (d *cephRBDVolumeDriver) goceph_renameRBDImage(pool, name, newname string) error {
	// build image struct
	rbdImage := rbd.GetImage(d.ioctx, name)

	// rename the block device image
	return rbdImage.Rename(newname)
}

//
// NOTE: the following are Shell commands for low level kernel RBD or Device
// operations - there are no go-ceph lib alternatives
//

// RBD subcommands

// mapImage will map the RBD Image to a kernel device
func (d *cephRBDVolumeDriver) mapImage(pool, imagename string) (string, error) {
	device, err := d.rbdsh(pool, "map", imagename)
	// NOTE: ubuntu rbd map seems to not return device. if no error, assume "default" /dev/rbd/<pool>/<image> device
	if device == "" && err == nil {
		device = fmt.Sprintf("/dev/rbd/%s/%s", pool, imagename)
	}

	return device, err
}

// unmapImageDevice will release the mapped kernel device
func (d *cephRBDVolumeDriver) unmapImageDevice(device string) error {
	// NOTE: this does not even require a user nor a pool, just device name
	_, err := d.rbdsh("", "unmap", device)
	return err
}

// Callouts to other unix shell commands: blkid, mount, umount

// deviceType identifies Image FS Type - requires RBD image to be mapped to kernel device
func (d *cephRBDVolumeDriver) deviceType(device string) (string, error) {
	// blkid Output:
	//	xfs
	blkid, err := shWithDefaultTimeout("blkid", "-o", "value", "-s", "TYPE", device)
	if err != nil {
		return "", err
	}
	if blkid != "" {
		return blkid, nil
	} else {
		return "", errors.New("Unable to determine device fs type from blkid")
	}
}

// verifyDeviceFilesystem will attempt to check XFS filesystems for errors
func (d *cephRBDVolumeDriver) verifyDeviceFilesystem(device, fstype string) error {
	if fstype != "xfs" {
		return nil
	}
	// "xfs_repair  -n  (no  modify node) will return a status of 1 if filesystem
	// corruption was detected and 0 if no filesystem corruption was detected." xfs_repair(8)
	// TODO: make sure /usr/sbin is in PATH?

	_, err := shWithDefaultTimeout("xfs_repair", "-n", device)
	if err != nil {
		switch err.(type) {
		case ShTimeoutError:
			// recover timeout errors - dont propagate
			return nil
		default:
			// assume any other error is xfs error (?)
			return err
		}
	}

	return nil
}

// mountDevice will call mount on kernel device with a docker volume subdirectory
func (d *cephRBDVolumeDriver) mountDevice(device, mountdir, fstype string) error {
	_, err := shWithDefaultTimeout("mount", "-t", fstype, device, mountdir)
	return err
}

// unmountDevice will call umount on kernel device to unmount from host's docker subdirectory
func (d *cephRBDVolumeDriver) unmountDevice(device string) error {
	_, err := shWithDefaultTimeout("umount", device)
	return err
}

// UTIL

// rbdsh will call rbd with the given command arguments, also adding config, user and pool flags
func (d *cephRBDVolumeDriver) rbdsh(pool, command string, args ...string) (string, error) {
	args = append([]string{"--conf", d.config, "--id", d.user, command}, args...)
	if pool != "" {
		args = append([]string{"--pool", pool}, args...)
	}
	return shWithDefaultTimeout("rbd", args...)
}

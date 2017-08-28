// Copyright 2015 YP LLC.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.
package main

/**
 * Ceph RBD Docker VolumeDriver Plugin
 *
 * rbd-docker-plugin service creates a UNIX socket that can accept Volume
 * Driver requests (JSON HTTP POSTs) from Docker Engine.
 *
 * Historical note: Due to some issues using the go-ceph library for
 * locking/unlocking, we reimplemented all functionality to use shell CLI
 * commands via the 'rbd' executable.
 *
 * System Requirements:
 *   - requires rbd CLI binary in PATH
 *
 * Plugin name: rbd  -- configurable via --name
 *
 * % docker run --volume-driver=rbd -v imagename:/mnt/dir IMAGE [CMD]
 *
 * golang github code examples:
 * - https://github.com/docker/docker/blob/master/experimental/plugins_volume.md
 * - https://github.com/docker/go-plugins-helpers/tree/master/volume
 * - https://github.com/calavera/docker-volume-glusterfs
 * - https://github.com/AcalephStorage/docker-volume-ceph-rbd
 */

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

	"github.com/docker/go-plugins-helpers/volume"
)

var (
	imageNameRegexp    = regexp.MustCompile(`^(([-_.[:alnum:]]+)/)?([-_.[:alnum:]]+)(@([0-9]+))?$`) // optional pool or size in image name
	rbdUnmapBusyRegexp = regexp.MustCompile(`^exit status 16$`)
)

// Volume is our local struct to store info about Ceph RBD Image
type Volume struct {
	Name   string // RBD Image name
	Device string // local host kernel device (e.g. /dev/rbd1)
	Locker string // track the lock name
	FStype string
	Pool   string
	ID     string
}

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
	volumes map[string]*Volume // track locally mounted volumes, key on mountpoint
	m       *sync.Mutex        // mutex to guard operations that change volume maps or use conn
}

// newCephRBDVolumeDriver builds the driver struct, reads config file and connects to cluster
func newCephRBDVolumeDriver(pluginName, cluster, userName, defaultPoolName, rootBase, config string) cephRBDVolumeDriver {
	// the root mount dir will be based on docker default root and plugin name - pool added later per volume
	mountDir := filepath.Join(rootBase, pluginName)
	log.Printf("INFO: newCephRBDVolumeDriver: setting base mount dir=%s", mountDir)

	// fill everything except the connection and context
	driver := cephRBDVolumeDriver{
		name:    pluginName,
		cluster: cluster,
		user:    userName,
		pool:    defaultPoolName,
		root:    mountDir,
		config:  config,
		volumes: map[string]*Volume{},
		m:       &sync.Mutex{},
	}

	return driver
}

// ************************************************************
//
// Implement the Docker Volume Driver interface
//
// Using https://github.com/docker/go-plugins-helpers/
//
// ************************************************************

// Capabilities
// Scope: global - images managed using rbd plugin can be considered "global"
func (d cephRBDVolumeDriver) Capabilities() *volume.CapabilitiesResponse {
	return &volume.CapabilitiesResponse{
		Capabilities: volume.Capability{
			Scope: "global",
		},
	}
}

// Create will ensure the RBD image requested is available.  Plugin requires
// --create option flag to be able to provision new RBD images.
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
func (d cephRBDVolumeDriver) Create(r *volume.CreateRequest) error {
	log.Printf("INFO: API Create(%q)", r)
	d.m.Lock()
	defer d.m.Unlock()

	return d.createImage(r)
}

func (d cephRBDVolumeDriver) createImage(r *volume.CreateRequest) error {
	log.Printf("INFO: createImage(%q)", r)

	fstype := *defaultImageFSType

	// parse image name optional/default pieces
	pool, name, size, err := d.parseImagePoolNameSize(r.Name)
	if err != nil {
		log.Printf("ERROR: parsing volume: %s", err)
		return err
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
		return nil
	}

	exists, err := d.rbdImageExists(pool, name)
	if err != nil {
		log.Printf("ERROR: checking for RBD Image: %s", err)
		return err
	}
	if !exists {
		if !*canCreateVolumes {
			errString := fmt.Sprintf("Ceph RBD Image not found: %s", name)
			log.Println("ERROR: " + errString)
			return errors.New(errString)
		}
		// try to create it ... use size and default fs-type
		err = d.createRBDImage(pool, name, size, fstype)
		if err != nil {
			errString := fmt.Sprintf("Unable to create Ceph RBD Image(%s): %s", name, err)
			log.Println("ERROR: " + errString)
			return errors.New(errString)
		}
	}

	return nil
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
func (d cephRBDVolumeDriver) Remove(r *volume.RemoveRequest) error {
	log.Printf("INFO: API Remove(%s)", r)
	d.m.Lock()
	defer d.m.Unlock()

	// parse full image name for optional/default pieces
	pool, name, _, err := d.parseImagePoolNameSize(r.Name)
	if err != nil {
		log.Printf("ERROR: parsing volume: %s", err)
		return err
	}

	mount := d.mountpoint(pool, name)

	// do we know about this volume? does it matter?
	if _, found := d.volumes[mount]; !found {
		log.Printf("WARN: Volume is not in known mounts: %s", mount)
	}

	exists, err := d.rbdImageExists(pool, name)
	if err != nil {
		log.Printf("ERROR: checking for RBD Image: %s", err)
		return err
	}
	if !exists {
		errString := fmt.Sprintf("Ceph RBD Image not found: %s", name)
		log.Println("ERROR: " + errString)
		return errors.New(errString)
	}

	// attempt to gain lock before remove - lock seems to disappear after rm (but not after rename)
	locker, err := d.lockImage(pool, name)
	if err != nil {
		errString := fmt.Sprintf("Unable to lock image for remove: %s", name)
		log.Println("ERROR: " + errString)
		return errors.New(errString)
	}

	// remove action can be: ignore, delete or rename
	if removeActionFlag == "delete" {
		// delete it (for real - destroy it ... )
		err = d.removeRBDImage(pool, name)
		if err != nil {
			errString := fmt.Sprintf("Unable to remove Ceph RBD Image(%s): %s", name, err)
			log.Println("ERROR: " + errString)
			defer d.unlockImage(pool, name, locker)
			return errors.New(errString)
		}
		defer d.unlockImage(pool, name, locker)
	} else if removeActionFlag == "rename" {
		// just rename it (in case needed later, or can be culled via script)
		// TODO: maybe add a timestamp?
		err = d.renameRBDImage(pool, name, "zz_"+name)
		if err != nil {
			errString := fmt.Sprintf("Unable to rename with zz_ prefix: RBD Image(%s): %s", name, err)
			log.Println("ERROR: " + errString)
			// unlock by old name
			defer d.unlockImage(pool, name, locker)
			return errors.New(errString)
		}
		// unlock by new name
		defer d.unlockImage(pool, "zz_"+name, locker)
	} else {
		// ignore the remove call - but unlock ?
		defer d.unlockImage(pool, name, locker)
	}

	delete(d.volumes, mount)
	return nil
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
func (d cephRBDVolumeDriver) Mount(r *volume.MountRequest) (*volume.MountResponse, error) {
	log.Printf("INFO: API Mount(%s)", r)
	d.m.Lock()
	defer d.m.Unlock()

	// parse full image name for optional/default pieces
	pool, name, _, err := d.parseImagePoolNameSize(r.Name)
	if err != nil {
		log.Printf("ERROR: parsing volume: %s", err)
		return nil, err
	}

	mount := d.mountpoint(pool, name)

	// attempt to lock
	locker, err := d.lockImage(pool, name)
	if err != nil {
		log.Printf("ERROR: locking RBD Image(%s): %s", name, err)
		return nil, errors.New("Unable to get Exclusive Lock")
	}

	// map and mount the RBD image -- these are OS level commands, not avail in go-ceph

	// map
	device, err := d.mapImage(pool, name)
	if err != nil {
		log.Printf("ERROR: mapping RBD Image(%s) to kernel device: %s", name, err)
		// failsafe: need to release lock
		defer d.unlockImage(pool, name, locker)
		return nil, errors.New("Unable to map kernel device")
	}

	// determine device FS type
	fstype, err := d.deviceType(device)
	if err != nil {
		log.Printf("WARN: unable to detect RBD Image(%s) fstype: %s", name, err)
		// NOTE: don't fail - FOR NOW we will assume default plugin fstype
		fstype = *defaultImageFSType
	}

	// double check image filesystem if possible
	err = d.verifyDeviceFilesystem(device, mount, fstype)
	if err != nil {
		log.Printf("ERROR: filesystem may need repairs: %s", err)
		// failsafe: need to release lock and unmap kernel device
		defer d.unmapImageDevice(device)
		defer d.unlockImage(pool, name, locker)
		return nil, errors.New("Image filesystem has errors, requires manual repairs")
	}

	// check for mountdir - create if necessary
	err = os.MkdirAll(mount, os.ModeDir|os.FileMode(int(0775)))
	if err != nil {
		log.Printf("ERROR: creating mount directory: %s", err)
		// failsafe: need to release lock and unmap kernel device
		defer d.unmapImageDevice(device)
		defer d.unlockImage(pool, name, locker)
		return nil, errors.New("Unable to make mountdir")
	}

	// mount
	err = d.mountDevice(fstype, device, mount)
	if err != nil {
		log.Printf("ERROR: mounting device(%s) to directory(%s): %s", device, mount, err)
		// need to release lock and unmap kernel device
		defer d.unmapImageDevice(device)
		defer d.unlockImage(pool, name, locker)
		return nil, errors.New("Unable to mount device")
	}

	// if all that was successful - add to our list of volumes
	d.volumes[mount] = &Volume{
		Name:   name,
		Device: device,
		Locker: locker,
		FStype: fstype,
		Pool:   pool,
		ID:     r.ID,
	}

	return &volume.MountResponse{Mountpoint: mount}, nil
}

// Get the list of volumes registered with the plugin.
// Default returns Ceph RBD images in default pool.
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
func (d cephRBDVolumeDriver) List() (*volume.ListResponse, error) {
	volNames, err := d.rbdList()
	if err != nil {
		return nil, err
	}
	vols := make([]*volume.Volume, 0, len(volNames))
	for _, name := range volNames {
		apiVol := &volume.Volume{Name: name}

		// for each known mounted vol, add Mountpoint
		// FIXME: assumes default rbd pool - should we keep track of all pools? query each? just assume one pool?
		mount := d.mountpoint(d.pool, name)
		_, ok := d.volumes[mount]
		if ok {
			apiVol.Mountpoint = mount
		}

		vols = append(vols, apiVol)
	}

	log.Printf("INFO: List request => %s", vols)
	return &volume.ListResponse{Volumes: vols}, nil
}

// rbdList performs an `rbd ls` on the default pool
func (d *cephRBDVolumeDriver) rbdList() ([]string, error) {
	result, err := d.rbdsh(d.pool, "ls")
	if err != nil {
		return nil, err
	}
	// split into lines - should be one rbd image name per line
	return strings.Split(result, "\n"), nil
}

// Get the volume info.
//
// POST /VolumeDriver.Get
//
// GetRequest:
//    { "Name": "volume_name" }
//    Docker needs reminding of the path to the volume on the host.
//
// GetResponse:
//    { "Volume": { "Name": "volume_name", "Mountpoint": "/path/to/directory/on/host" }}
//
func (d cephRBDVolumeDriver) Get(r *volume.GetRequest) (*volume.GetResponse, error) {
	// parse full image name for optional/default pieces
	pool, name, _, err := d.parseImagePoolNameSize(r.Name)
	if err != nil {
		log.Printf("ERROR: parsing volume: %s", err)
		return nil, err
	}

	// Check to see if the image exists
	exists, err := d.rbdImageExists(pool, name)
	if err != nil {
		log.Printf("WARN: checking for RBD Image: %s", err)
		return nil, err
	}
	mountPath := d.mountpoint(pool, name)
	if !exists {
		log.Printf("WARN: Image %s does not exist", r.Name)
		delete(d.volumes, mountPath)
		return nil, fmt.Errorf("Image %s does not exist", r.Name)
	}

	// for each mounted vol, keep Mountpoint
	_, ok := d.volumes[mountPath]
	if !ok {
		mountPath = ""
	}
	log.Printf("INFO: Get request(%s) => %s", name, mountPath)

	return &volume.GetResponse{Volume: &volume.Volume{Name: r.Name, Mountpoint: mountPath}}, nil
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
// FIXME: does volume API require error if Volume requested does not exist/is not mounted? Similar to List/Get leaving mountpoint empty?
//
func (d cephRBDVolumeDriver) Path(r *volume.PathRequest) (*volume.PathResponse, error) {
	// parse full image name for optional/default pieces
	pool, name, _, err := d.parseImagePoolNameSize(r.Name)
	if err != nil {
		log.Printf("ERROR: parsing volume: %s", err)
		return nil, err
	}

	mountPath := d.mountpoint(pool, name)
	log.Printf("INFO: API Path request(%s) => %s", name, mountPath)
	return &volume.PathResponse{Mountpoint: mountPath}, nil
}

// POST /VolumeDriver.Unmount
//
// - assuming writes are finished and no other containers using same disk on this host?

// Request:
//    { "Name": "volume_name", ID: "client-id" }
//    Indication that Docker no longer is using the named volume. This is
//    called once per container stop. Plugin may deduce that it is safe to
//    deprovision it at this point.
//
// Response:
//    Respond with error or nil
//
func (d cephRBDVolumeDriver) Unmount(r *volume.UnmountRequest) error {
	log.Printf("INFO: API Unmount(%s)", r)
	d.m.Lock()
	defer d.m.Unlock()

	var err_msgs = []string{}

	// parse full image name for optional/default pieces
	pool, name, _, err := d.parseImagePoolNameSize(r.Name)
	if err != nil {
		log.Printf("ERROR: parsing volume: %s", err)
		return err
	}

	mount := d.mountpoint(pool, name)

	// check if it's in our mounts - we may not know about it if plugin was started late?
	vol, found := d.volumes[mount]
	if !found {
		// FIXME: is this an error or just a log and a return nil?
		//return fmt.Errorf("WARN: Volume is not in known mounts: ignoring request to unmount: %s/%s", pool, name)
		log.Printf("WARN: Volume is not in known mounts: ignoring request to unmount: %s/%s", pool, name)
		return nil
		/**
		// set up a fake Volume with defaults ...
		// - device is /dev/rbd/<pool>/<image> in newer ceph versions
		// - assume we are the locker (will fail if locked from another host)
		vol = &Volume{
			Pool:   pool,
			Name:   name,
			Device: fmt.Sprintf("/dev/rbd/%s/%s", pool, name),
			Locker: d.localLockerCookie(),
			ID, r.ID,
		}
		*/
	}

	// if found - double check ID
	if vol.ID != r.ID {
		log.Printf("WARN: Volume client ID(%s) does not match requestor id(%s) for %s/%s",
			vol.ID, r.ID, pool, name)
		return nil
	}

	// unmount
	// NOTE: this might succeed even if device is still in use inside container. device will dissappear from host side but still be usable inside container :(
	err = d.unmountDevice(vol.Device)
	if err != nil {
		log.Printf("ERROR: unmounting device(%s): %s", vol.Device, err)
		// failsafe: will still attempt to unmap and unlock
		err_msgs = append(err_msgs, "Error unmounting device")
	}

	// unmap
	err = d.unmapImageDevice(vol.Device)
	if err != nil {
		log.Printf("ERROR: unmapping image device(%s): %s", vol.Device, err)
		// NOTE: rbd unmap exits 16 if device is still being used - unlike umount.  try to recover differently in that case
		if rbdUnmapBusyRegexp.MatchString(err.Error()) {
			// can't always re-mount and not sure if we should here ... will be cleaned up once original container goes away
			log.Printf("WARN: unmap failed due to busy device, early exit from this Unmount request.")
			return err
		}
		// other error, failsafe: proceed to attempt to unlock
		err_msgs = append(err_msgs, "Error unmapping kernel device")
	}

	// unlock
	err = d.unlockImage(vol.Pool, vol.Name, vol.Locker)
	if err != nil {
		log.Printf("ERROR: unlocking RBD image(%s): %s", vol.Name, err)
		err_msgs = append(err_msgs, "Error unlocking image")
	}

	// forget it
	delete(d.volumes, mount)

	// check for piled up errors
	if len(err_msgs) > 0 {
		return errors.New(strings.Join(err_msgs, ", "))
	}

	return nil
}

//
// END Docker VolumeDriver Plugin API methods
//
// ***************************************************************************
// ***************************************************************************
//

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
	_, err := d.rbdsh(pool, "info", findName)
	if err != nil {
		// NOTE: even though method signature returns err - we take the error
		// in this instance as the indication that the image does not exist
		// TODO: can we double check exit value for exit status 2 ?
		return false, nil
	}
	return true, nil
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

	// create the block device image with format=2 (v2) - features seem heavily dependent on version and configuration of RBD pools
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

	// TODO: should we chown/chmod the directory? e.g. non-root container users
	// won't be able to write. where to get the preferred user id?

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
	// check the output for a lock -- if blank or error, assume not locked (?)
	out, err := d.rbdsh(pool, "lock", "ls", name)
	if err != nil || out != "" {
		return false, err
	}
	// otherwise - no error and output is not blank - assume a lock exists ...
	return true, nil
}

// lockImage locks image and returns locker cookie name
func (d *cephRBDVolumeDriver) lockImage(pool, imagename string) (string, error) {
	cookie := d.localLockerCookie()
	_, err := d.rbdsh(pool, "lock", "add", imagename, cookie)
	if err != nil {
		return "", err
	}
	return cookie, nil
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

// removeRBDImage will remove a Ceph RBD image - no undo available
func (d *cephRBDVolumeDriver) removeRBDImage(pool, name string) error {
	log.Println("INFO: Remove RBD Image(%s/%s)", pool, name)

	// remove the block device image
	_, err := d.rbdsh(pool, "rm", name)

	if err != nil {
		return err
	}
	return nil
}

// renameRBDImage will move a Ceph RBD image to new name
func (d *cephRBDVolumeDriver) renameRBDImage(pool, name, newname string) error {
	log.Println("INFO: Rename RBD Image(%s/%s -> %s)", pool, name, newname)

	out, err := d.rbdsh(pool, "rename", name, newname)
	if err != nil {
		log.Printf("ERROR: unable to rename: %s: %s", err, out)
		return err
	}
	return nil
}

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
func (d *cephRBDVolumeDriver) verifyDeviceFilesystem(device, mount, fstype string) error {
	// for now we only handle XFS
	// TODO: use fsck for ext4?
	if fstype != "xfs" {
		return nil
	}

	// check XFS volume
	err := d.xfsRepairDryRun(device)
	if err != nil {
		switch err.(type) {
		case ShTimeoutError:
			// propagate timeout errors - can't recover? system error? don't try to mount at that point
			return err
		default:
			// assume any other error is xfs error and attempt limited repair
			return d.attemptLimitedXFSRepair(fstype, device, mount)
		}
	}

	return nil
}

func (d *cephRBDVolumeDriver) xfsRepairDryRun(device string) error {
	// "xfs_repair  -n  (no  modify node) will return a status of 1 if filesystem
	// corruption was detected and 0 if no filesystem corruption was detected." xfs_repair(8)
	// TODO: can we check cmd output and ensure the mount/unmount is suggested by stale disk log?

	_, err := shWithDefaultTimeout("xfs_repair", "-n", device)
	return err
}

// attemptLimitedXFSRepair will try mount/unmount and return result of another xfs-repair-n
func (d *cephRBDVolumeDriver) attemptLimitedXFSRepair(fstype, device, mount string) (err error) {
	log.Printf("WARN: attempting limited XFS repair (mount/unmount) of %s  %s", device, mount)

	// mount
	err = d.mountDevice(fstype, device, mount)
	if err != nil {
		return err
	}

	// unmount
	err = d.unmountDevice(device)
	if err != nil {
		return err
	}

	// try a dry-run again and return result
	return d.xfsRepairDryRun(device)
}

// mountDevice will call mount on kernel device with a docker volume subdirectory
func (d *cephRBDVolumeDriver) mountDevice(fstype, device, mountdir string) error {
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

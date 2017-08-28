# Simple Ceph RBD Docker VolumeDriver Plugin

* Use Case: Persistent Storage for a Single Docker Container
  * one RBD Image can only be used by one Docker Container at a time

* Plugin is a separate process running alongside Docker Daemon
  * plugin can be configured for a single Ceph User
  * run multiple plugin instances for varying configs (ceph user, default pool, default size)
  * OPTIONAL: pass extra config via volume name to override default pool and creation size:
    * docker run --volume-driver rbd -v poolname/imagename@size:/mnt/disk1 ...

* plugin supports all Docker VolumeDriver Plugin API commands (Volume Plugin API v1.12.x)
  * Create - can provision Ceph RBD Image in a pool of a certain size
    * controlled by `--create` boolean flag (default false)
    * default size from `--size` flag (default 20480 = 20GB)
  * Mount - Locks, Maps and Mounts RBD Image to the Host system
  * Unmount - Unmounts, Unmaps and Unlocks the RBD Image on request
  * Remove - Removes (destroys) RBD Image on request
    * only called for `docker run --rm -v ...` or `docker rm -v ...`
    * action controlled by plugin's `--remove` flag, which can be one of three values:
      - ''ignore'' - the call to delete the ceph rbd volume is ignored (default)
      - ''rename'' - will cause image to be renamed with _zz_ prefix for later culling
      - ''delete'' - will actually delete ceph rbd image (destructive)
  * Get, List - Return information on accessible RBD volumes

## Plugin Setup

Plugin is a standalone process and places a Socket file in a known location;
needs to start before Docker.  It does not daemonize by default, so if you need
it in the background, use normal shell process control (&).

The driver has a name, also used to name the socket, which is used to refer to
the plugin via the `--volume-driver=name` docker CLI option, allowing multiple
uniquely named plugin instances with different default configurations.

For the default name is "rbd", use `--volume-driver rbd` from docker.

General build/run requirements:
* /usr/bin/rbd for manipulating Ceph RBD images
* /usr/sbin/mkfs.xfs for fs creation (default fstype)
* /usr/bin/mount and /usr/bin/umount
* golang/dep tool

Tested with Ceph version 0.94.2 on Centos 7.1 host with Docker 1.12

### Building rbd-docker-plugin

Clone the repo and use the Makefile:

    make

To get `dist/rbd-docker-plugin` binary.

Or the equivalent shell commands:

    go get -u github.com/golang/dep/cmd/dep
    dep ensure
    go build -v -x -o dist/rbd-docker-plugin .

If none of the dependencies has changed (??) you might be able to get away with:

    go get github.com/porcupie/rbd-docker-plugin


### Commandline Options

    Usage of ./rbd-docker-plugin:
      --ceph-user="admin": Ceph user to use for RBD
      --create=false: Can auto Create RBD Images (default: false)
      --fs="xfs": FS type for the created RBD Image (must have mkfs.type)
      --logdir="/var/log": Logfile directory for RBD Docker Plugin
      --mount="/var/lib/docker/volumes": Mount directory for volumes on host
      --name="rbd": Docker plugin name for use on --volume-driver option
      --pool="rbd": Default Ceph Pool for RBD operations
      --remove=false: Can Remove (destroy) RBD Images (default: false, volume will be renamed zz_name)
      --size=20480: RBD Image size to Create (in MB) (default: 20480=20GB

### Start the Plugin

Start with the default options:

* socket name=rbd, pool=rbd, user=admin, logfile=/var/log/rbd-docker-plugin.log
* no creation or removal of volumes

    sudo rbd-docker-plugin

For Debugging: send log to STDERR:

    sudo RBD_DOCKER_PLUGIN_DEBUG=1 rbd-docker-plugin

Use a different socket name and Ceph pool

    sudo rbd-docker-plugin --name rbd2 --pool liverpool
    # docker run --volume-driver rbd2 -v ...

To allow creation of new RBD Images:

    sudo rbd-docker-plugin --create

To allow creation and removal:

    sudo rbd-docker-plugin --create --remove

Then you would be able to use RBD volumes via Docker CLI:

    docker run --volume-driver rbd -v ...

### Testing

Can test using docker engine 1.8+ which has `--volume-driver` support.

* https://docker.com/

Alternatively, you can POST json to the socket to manually test.  If your curl
is new enough (v7.40+), you can use the `--unix-socket` option and syntax.  You
can also use [this golang version](https://github.com/Soulou/curl-unix-socket)
instead:

    go get github.com/Soulou/curl-unix-socket


Once you have that you can POST json to the plugin:

    % sudo curl-unix-socket -v -X POST unix:///run/docker/plugins/rbd.sock:/Plugin.Activate
    > POST /Plugin.Activate HTTP/1.1
    > Socket: /run/docker/plugins/rbd.sock
    > Content-Length: 0
    >
    < HTTP/1.1 200 OK
    < Content-Type: appplication/vnd.docker.plugins.v1+json
    < Date: Tue, 28 Jul 2015 18:52:11 GMT
    < Content-Length: 33
    {"Implements": ["VolumeDriver"]}


    # Plugin started without --create:
    % sudo curl-unix-socket -v -X POST -d '{"Name": "testimage"}' unix:///run/docker/plugins/rbd.sock:/VolumeDriver.Create
    > POST /VolumeDriver.Create HTTP/1.1
    > Socket: /run/docker/plugins/rbd.sock
    > Content-Length: 21
    >
    < HTTP/1.1 500 Internal Server Error
    < Content-Length: 62
    < Content-Type: appplication/vnd.docker.plugins.v1+json
    < Date: Tue, 28 Jul 2015 18:53:20 GMT
    {"Mountpoint":"","Err":"Ceph RBD Image not found: testimage"}

    # Plugin started --create turned on will create unknown image:
    % sudo curl-unix-socket -v -X POST -d '{"Name": "testimage"}' unix:///run/docker/plugins/rbd.sock:/VolumeDriver.Create
    > POST /VolumeDriver.Create HTTP/1.1
    > Socket: /run/docker/plugins/rbd.sock
    > Content-Length: 21
    >
    < HTTP/1.1 200 OK
    < Content-Length: 27
    < Content-Type: appplication/vnd.docker.plugins.v1+json
    < Date: Fri, 14 Aug 2015 19:47:35 GMT
    {"Mountpoint":"","Err":""}

## Examples

If you need persistent storage for your application container, you can use a
Ceph Rados Block Device (RBD) as a persistent disk.

You can provision the Block Device and Filesystem first, or allow a
sufficiently configured Plugin instance create it for you.  This plugin can
create RBD images with XFS filesystem.

1. (Optional) Provision RBD Storage yourself
  * `sudo rbd create --size 1024 foo`
  * `sudo rbd map foo`  => /dev/rbd1
  * `sudo mkfs.xfs /dev/rbd1`
  * `sudo rbd unmap /dev/rbd1`
2. Or Run the RBD Docker Plugin with `--create` option flag and just request a volume
  * `sudo rbd-docker-plugin --create`
3. Requesting and Using Volumes
  * `docker run --volume-driver=rbd --volume foo:/mnt/foo -it ubuntu /bin/bash`
  * Volume "foo" will be locked, mapped and mounted to Host and bind-mounted to container at `/mnt/foo`
  * When container exits, the volume will be unmounted, unmapped and unlocked
  * You can control the RBD Pool and initial Size using this syntax sugar:
    * foo@1024 => pool=rbd (default), image=foo, size 1GB
    * deep/foo =>  pool=deep, image=foo and default `--size` (20GB)
    * deep/foo@1024 => pool=deep, image=foo, size 1GB
    - pool must already exist

### Misc

* Create RBD Snapshots: `sudo rbd snap create --image foo --snap foosnap`
* Resize RBD Volume:
  * set max size: `sudo rbd resize --size 2048 --image foo`
  * map/mount and then fix XFS: `sudo xfs_growfs -d /mnt/foo`


## Links


- [Legacy Plugins](https://docs.docker.com/engine/extend/legacy_plugins/)
  - [Volume plugins](https://docs.docker.com/engine/extend/plugins_volume/)

# Packaging

Using [tpkg](http://tpkg.github.io) to distribute and specify native package
dependencies.  Tested with Centos 7.1 and yum/rpm packages.


# License

This project is using the MIT License (MIT), see LICENSE file.

Copyright (c) 2015 YP LLC

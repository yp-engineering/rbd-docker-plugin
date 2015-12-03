# Change Log
All notable changes to project should be documented in this file.
We attempt to adhere to [Semantic Versioning](http://semver.org/).

## [Unreleased]

## [0.4.0] - 2015-12-03
### Changed
- Last ditch effort : Update all Plugin RBD functions to use CLI shell
  commands instead of go-ceph library
- Provide command line flag --go-ceph to use go-ceph lib, otherwise default
  now is shell CLI command via `rbd` binary

## [0.3.1] - 2015-11-30
### Changed
- Try to open RBD Image without read-only option (no effect)
- Try to use same client-id for every connection -- not possible in
  go-ceph
- Adding --conf options to external rbd operations (was having micro-osd
  issues)

## [0.3.0] - 2015-11-25
### Changed
- Update go-ceph import to use github.com/ceph/go-ceph instead of
  noahdesu/go-ceph
- Recreate ceph connection and pool context for every operation (don't 
  try to cache them)

## [0.2.2] - 2015-11-19
### Changed
- Disable the reload operation in systemd service unit, having issues
  with go-ceph lib and that operation (panics)
- Update the Image Rename and Remove functions to use go-ceph lib
  instead of shelling out to rbd binary
- Update the tpkg scripts to start the service on installation

## [0.2.1] - 2015-09-11
### Added
- Merged pull request with some RPM scripts for use in generic Redhat EL7 (Thanks Clement Laforet <sheepkiller@cotds.org>)

## [0.2.0] - 2015-08-25
### Changed
- Added micro-osd script for testing Ceph locally

## [0.1.9] - 2015-08-20
### Changed
- Added user ID and options to more shell rbd binary exec commands (Thanks Sébastien Han <seb@redhat.com>)
- Moving version definition from tpkg.yml to version.go
- Better blkid integration (Thanks Sébastien Han <seb@redhat.com>)

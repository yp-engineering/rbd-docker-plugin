// Copyright 2015 YP LLC.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.
package main

// Ceph RBD VolumeDriver Docker Plugin, setup config and go

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	dkvolume "github.com/docker/go-plugins-helpers/volume"
)

var (
	VALID_REMOVE_ACTIONS = []string{"ignore", "delete", "rename"}

	// Plugin Option Flags
	versionFlag        = flag.Bool("version", false, "Print version")
	debugFlag          = flag.Bool("debug", false, "Debug output")
	pluginName         = flag.String("name", "rbd", "Docker plugin name for use on --volume-driver option")
	cephUser           = flag.String("user", "admin", "Ceph user")
	cephConfigFile     = flag.String("config", "/etc/ceph/ceph.conf", "Ceph cluster config") // more likely to have config file pointing to cluster
	cephCluster        = flag.String("cluster", "", "Ceph cluster")                          // less likely to run multiple clusters on same hardware
	defaultCephPool    = flag.String("pool", "rbd", "Default Ceph Pool for RBD operations")
	pluginDir          = flag.String("plugins", "/run/docker/plugins", "Docker plugin directory for socket")
	rootMountDir       = flag.String("mount", dkvolume.DefaultDockerRootDirectory, "Mount directory for volumes on host")
	logDir             = flag.String("logdir", "/var/log", "Logfile directory")
	canCreateVolumes   = flag.Bool("create", false, "Can auto Create RBD Images")
	defaultImageSizeMB = flag.Int("size", 20*1024, "RBD Image size to Create (in MB) (default: 20480=20GB)")
	defaultImageFSType = flag.String("fs", "xfs", "FS type for the created RBD Image (must have mkfs.type)")
	useGoCeph          = flag.Bool("go-ceph", false, "Use go-ceph library (default: false)")
)

// setup a validating flag for remove action
type removeAction string

func (a *removeAction) String() string {
	return string(*a)
}

func (a *removeAction) Set(value string) error {
	if !contains(VALID_REMOVE_ACTIONS, value) {
		return errors.New(fmt.Sprintf("Invalid value: %s, valid values are: %q", value, VALID_REMOVE_ACTIONS))
	}
	*a = removeAction(value)
	return nil
}

func contains(vals []string, check string) bool {
	for _, v := range vals {
		if check == v {
			return true
		}
	}
	return false
}

var removeActionFlag removeAction = "ignore"

func init() {
	flag.Var(&removeActionFlag, "remove", "Action to take on Remove: ignore, delete or rename")
	flag.Parse()
}

func socketPath() string {
	return filepath.Join(*pluginDir, *pluginName+".sock")
}

func logfilePath() string {
	return filepath.Join(*logDir, *pluginName+"-docker-plugin.log")
}

func main() {
	if *versionFlag {
		fmt.Printf("%s\n", VERSION)
		return
	}

	logFile, err := setupLogging()
	if err != nil {
		log.Fatalf("FATAL: Unable to setup logging: %s", err)
	}
	defer shutdownLogging(logFile)

	log.Printf("INFO: starting rbd-docker-plugin version %s", VERSION)
	log.Printf("INFO: canCreateVolumes=%q, removeAction=%q", *canCreateVolumes, removeActionFlag)
	log.Printf(
		"INFO: Setting up Ceph Driver for PluginID=%s, cluster=%s, user=%s, pool=%s, mount=%s, config=%s, go-ceph=%s",
		*pluginName,
		*cephCluster,
		*cephUser,
		*defaultCephPool,
		*rootMountDir,
		*cephConfigFile,
		*useGoCeph,
	)

	// double check for config file - required especially for non-standard configs
	if *cephConfigFile == "" {
		log.Fatal("FATAL: Unable to use ceph rbd tool without config file")
	}
	if _, err = os.Stat(*cephConfigFile); os.IsNotExist(err) {
		log.Fatalf("FATAL: Unable to find ceph config needed for ceph rbd tool: %s", err)
	}

	// build driver struct -- but don't create connection yet
	d := newCephRBDVolumeDriver(
		*pluginName,
		*cephCluster,
		*cephUser,
		*defaultCephPool,
		*rootMountDir,
		*cephConfigFile,
		*useGoCeph,
	)
	if *useGoCeph {
		defer d.shutdown()
	}

	log.Println("INFO: Creating Docker VolumeDriver Handler")
	h := dkvolume.NewHandler(d)

	socket := socketPath()
	log.Printf("INFO: Opening Socket for Docker to connect: %s", socket)
	// ensure directory exists
	err = os.MkdirAll(filepath.Dir(socket), os.ModeDir)
	if err != nil {
		log.Fatalf("FATAL: Error creating socket directory: %s", err)
	}

	// setup signal handling after logging setup and creating driver, in order to signal the logfile and ceph connection
	// NOTE: systemd will send SIGTERM followed by SIGKILL after a timeout to stop a service daemon
	signalChannel := make(chan os.Signal, 2) // chan with buffer size 2
	signal.Notify(signalChannel, syscall.SIGTERM, syscall.SIGKILL)
	go func() {
		for sig := range signalChannel {
			//sig := <-signalChannel
			switch sig {
			case syscall.SIGTERM, syscall.SIGKILL:
				log.Printf("INFO: received TERM or KILL signal: %s", sig)
				// close up conn and logs
				if *useGoCeph {
					d.shutdown()
				}
				shutdownLogging(logFile)
				os.Exit(0)
			}
		}
	}()

	// NOTE: pass empty string for group to skip broken chgrp in dkvolume lib
	err = h.ServeUnix("", socket)

	if err != nil {
		log.Printf("ERROR: Unable to create UNIX socket: %v", err)
	}
}

// isDebugEnabled checks for RBD_DOCKER_PLUGIN_DEBUG environment variable
func isDebugEnabled() bool {
	return *debugFlag || os.Getenv("RBD_DOCKER_PLUGIN_DEBUG") == "1"
}

// setupLogging attempts to log to a file, otherwise stderr
func setupLogging() (*os.File, error) {
	// use date, time and filename for log output
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// setup logfile - path is set from logfileDir and pluginName
	logfileName := logfilePath()
	if !isDebugEnabled() && logfileName != "" {
		logFile, err := os.OpenFile(logfileName, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
		if err != nil {
			// check if we can write to directory - otherwise just log to stderr?
			if os.IsPermission(err) {
				log.Printf("WARN: logging fallback to STDERR: %v", err)
			} else {
				// some other, more extreme system error
				return nil, err
			}
		} else {
			log.Printf("INFO: setting log file: %s", logfileName)
			log.SetOutput(logFile)
			return logFile, nil
		}
	}
	return nil, nil
}

func shutdownLogging(logFile *os.File) {
	// flush and close the file
	if logFile != nil {
		log.Println("INFO: closing log file")
		logFile.Sync()
		logFile.Close()
	}
}

func reloadLogging(logFile *os.File) (*os.File, error) {
	log.Println("INFO: reloading log")
	if logFile != nil {
		shutdownLogging(logFile)
	}
	return setupLogging()
}

// Copyright 2015 YP LLC.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.
package main

// Ceph RBD VolumeDriver Docker Plugin, setup config and go

// TODO: should we use logrus?  already imported in dkvolume and go-ceph *shrug*
import (
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
	// Plugin Option Flags
	versionFlag        = flag.Bool("version", false, "Print version")
	pluginName         = flag.String("name", "rbd", "Docker plugin name for use on --volume-driver option")
	cephUser           = flag.String("user", "admin", "Ceph user")
	cephConfigFile     = flag.String("config", "", "Ceph cluster config") // more likely to have config file pointing to cluster
	cephCluster        = flag.String("cluster", "", "Ceph cluster")       // less likely to run multiple clusters on same hardware
	defaultCephPool    = flag.String("pool", "rbd", "Default Ceph Pool for RBD operations")
	pluginDir          = flag.String("plugins", "/run/docker/plugins", "Docker plugin directory for socket")
	rootMountDir       = flag.String("mount", dkvolume.DefaultDockerRootDirectory, "Mount directory for volumes on host")
	logDir             = flag.String("logdir", "/var/log", "Logfile directory")
	canCreateVolumes   = flag.Bool("create", false, "Can auto Create RBD Images")
	canRemoveVolumes   = flag.Bool("remove", false, "Can Remove (destroy) RBD Images (default: false, volume will be renamed zz_name)")
	defaultImageSizeMB = flag.Int("size", 20*1024, "RBD Image size to Create (in MB) (default: 20480=20GB)")
	defaultImageFSType = flag.String("fs", "xfs", "FS type for the created RBD Image (must have mkfs.type)")
)

func init() {
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
		log.Panicf("Unable to setup logging: %s", err)
	}
	defer shutdownLogging(logFile)

	log.Printf(
		"INFO: Setting up Ceph Driver for PluginID=%s, cluster=%s, user=%s, pool=%s, mount=%s, config=%s",
		*pluginName,
		*cephCluster,
		*cephUser,
		*defaultCephPool,
		*rootMountDir,
		*cephConfigFile,
	)
	// build driver struct
	d := newCephRBDVolumeDriver(
		*pluginName,
		*cephCluster,
		*cephUser,
		*defaultCephPool,
		*rootMountDir,
		*cephConfigFile,
	)
	defer d.shutdown()

	log.Println("INFO: Creating Docker VolumeDriver Handler")
	h := dkvolume.NewHandler(d)

	socket := socketPath()
	log.Printf("INFO: Opening Socket for Docker to connect: %s", socket)
	// ensure directory exists
	err = os.MkdirAll(filepath.Dir(socket), os.ModeDir)
	if err != nil {
		log.Panicf("Error creating socket directory: %s", err)
	}

	// setup signal handling after logging setup and creating driver, in order to signal the logfile and ceph connection
	// NOTE: systemd will send SIGTERM followed by SIGKILL after a timeout to stop a service daemon
	// NOTE: we chose to use SIGHUP to reload logfile and ceph connection
	signalChannel := make(chan os.Signal, 2) // chan with buffer size 2
	signal.Notify(signalChannel, syscall.SIGTERM, syscall.SIGKILL, syscall.SIGHUP)
	go func() {
		for sig := range signalChannel {
			//sig := <-signalChannel
			switch sig {
			case syscall.SIGTERM, syscall.SIGKILL:
				log.Printf("INFO: received TERM or KILL signal: %s", sig)
				// close up conn and logs
				d.shutdown()
				shutdownLogging(logFile)
				os.Exit(0)
			case syscall.SIGHUP:
				// reload logs and conn
				log.Printf("INFO: received HUP signal: %s", sig)
				logFile, err = reloadLogging(logFile)
				if err != nil {
					log.Printf("Unable to reload log: %s", err)
				}
				d.reload()
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
	return os.Getenv("RBD_DOCKER_PLUGIN_DEBUG") == "1"
}

// setupLogging attempts to log to a file, otherwise stderr
func setupLogging() (*os.File, error) {
	// use date, time and filename for log output
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.SetPrefix(*pluginName + "-volume-plugin: ")

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

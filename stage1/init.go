package main

// this implements /init of stage1/host_nspawn-systemd

import (
	"fmt"
	"os"
	"syscall"

	"github.com/coreos-inc/rkt/pkg/mount"
	"github.com/docker/libcontainer/devices"
	"github.com/coreos-inc/rkt/rkt"
)

const (
	// Path to systemd-nspawn binary within the stage1 rootfs
	nspawnBin = "/usr/bin/systemd-nspawn"
)

func main() {
	root := "."
	debug := len(os.Args) > 1 && os.Args[1] == "debug"

	c, err := LoadContainer(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load container: %v\n", err)
		os.Exit(1)
	}

	if err = c.ContainerToSystemd(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to configure systemd: %v\n", err)
		os.Exit(2)
	}

	mc := mount.MountConfig{
		NoPivotRoot: true,
		DeviceNodes: []*devices.Device{
			// /dev/null and zero
			{
				Path:              "/dev/null",
				Type:              'c',
				MajorNumber:       1,
				MinorNumber:       3,
				CgroupPermissions: "rwm",
				FileMode:          0666,
			},
			{
				Path:              "/dev/zero",
				Type:              'c',
				MajorNumber:       1,
				MinorNumber:       5,
				CgroupPermissions: "rwm",
				FileMode:          0666,
			},

			{
				Path:              "/dev/full",
				Type:              'c',
				MajorNumber:       1,
				MinorNumber:       7,
				CgroupPermissions: "rwm",
				FileMode:          0666,
			},

			// consoles and ttys
			{
				Path:              "/dev/tty",
				Type:              'c',
				MajorNumber:       5,
				MinorNumber:       0,
				CgroupPermissions: "rwm",
				FileMode:          0666,
			},

			// /dev/urandom,/dev/random
			{
				Path:              "/dev/urandom",
				Type:              'c',
				MajorNumber:       1,
				MinorNumber:       9,
				CgroupPermissions: "rwm",
				FileMode:          0666,
			},
			{
				Path:              "/dev/random",
				Type:              'c',
				MajorNumber:       1,
				MinorNumber:       8,
				CgroupPermissions: "rwm",
				FileMode:          0666,
			},
		},
	}
	println(rkt.Stage1RootfsPath(c.Root))
	if err = mount.InitializeMountNamespace(rkt.Stage1RootfsPath(c.Root), false, &mc); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize mount namespace to %s: %v\n", rkt.Stage1RootfsPath(c.Root), err)
		os.Exit(2)

	}

	// TODO(philips): compile a static version of systemd-nspawn with this
	// stupidity patched out
	_, err = os.Stat("/run/systemd/system")
	if os.IsNotExist(err) {
		os.MkdirAll("/run/systemd/system", 0755)
	}
	os.MkdirAll("/tmp", 0755)

	ex := nspawnBin
	if _, err := os.Stat(ex); err != nil {
		fmt.Fprintf(os.Stderr, "Failed locating nspawn: %v\n", err)
		os.Exit(3)
	}

	args := []string{
		ex,
		"--help",              // Launch systemd in the container
		"--boot",              // Launch systemd in the container
		"--register", "false", // We cannot assume the host system is running a compatible systemd
	}

	if !debug {
		args = append(args, "--quiet") // silence most nspawn output (log_warning is currently not covered by this)
	}

	nsargs, err := c.ContainerToNspawnArgs()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to generate nspawn args: %v\n", err)
		os.Exit(4)
	}
	args = append(args, nsargs...)

	// Arguments to systemd
	args = append(args, "--help")
	args = append(args, "--")
	args = append(args, "--default-standard-output=tty") // redirect all service logs straight to tty
	if !debug {
		args = append(args, "--log-target=null") // silence systemd output inside container
		args = append(args, "--show-status=0")   // silence systemd initialization status output
	}

	env := os.Environ()

	if err := syscall.Exec(ex, args, env); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to execute %s %v: %v\n", ex, args, err)
		os.Exit(6)
	}
}

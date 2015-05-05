// Copyright 2014 CoreOS, Inc.
// Copyright 2015 Intel Corp
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//+build linux

package main

// this implements /init of stage1/nspawn+systemd

import (
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"strings"

	"github.com/coreos/rkt/Godeps/_workspace/src/github.com/appc/spec/schema/types"

	"github.com/coreos/rkt/common"
	"github.com/coreos/rkt/networking"
	"github.com/coreos/rkt/pkg/sys"
)

const (
	// Path to systemd-nspawn binary within the stage1 rootfs
	nspawnBin = "/usr/bin/systemd-nspawn"
	// Path to lkvm binary within the stage1 rootfs
	lkvmBin = "/usr/bin/lkvm"
	bzImg = "/usr/lib/kernel/vmlinux.container"
	// Path to the interpreter within the stage1 rootfs
	interpBin = "/usr/lib/ld-linux-x86-64.so.2"
	// Path to the localtime file/symlink in host
	localtimePath = "/etc/localtime"
)

// mirrorLocalZoneInfo tries to reproduce the /etc/localtime target in stage1/ to satisfy systemd-nspawn
func mirrorLocalZoneInfo(root string) {
	zif, err := os.Readlink(localtimePath)
	if err != nil {
		return
	}

	// On some systems /etc/localtime is a relative symlink, make it absolute
	if !filepath.IsAbs(zif) {
		zif = filepath.Join(filepath.Dir(localtimePath), zif)
		zif = filepath.Clean(zif)
	}

	src, err := os.Open(zif)
	if err != nil {
		return
	}
	defer src.Close()

	destp := filepath.Join(common.Stage1RootfsPath(root), zif)

	if err = os.MkdirAll(filepath.Dir(destp), 0755); err != nil {
		return
	}

	dest, err := os.OpenFile(destp, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer dest.Close()

	_, _ = io.Copy(dest, src)
}

var (
	debug       bool
	privNet     bool
	interactive bool
	virtualisation string
)

func init() {
	flag.BoolVar(&debug, "debug", false, "Run in debug mode")
	flag.BoolVar(&privNet, "private-net", false, "Setup private network")
	flag.BoolVar(&interactive, "interactive", false, "The pod is interactive")
	flag.StringVar(&virtualisation, "containment-type", "kvm", "Containment type to use: nspawn or kvm (default)")

	if os.Getenv("RKT_CONTAINMENT_TYPE") != "" {
		virtualisation = os.Getenv("RKT_CONTAINMENT_TYPE")
	}

	// this ensures that main runs only on main thread (thread group leader).
	// since namespace ops (unshare, setns) are done for a single thread, we
	// must ensure that the goroutine does not jump from OS thread to thread
	runtime.LockOSThread()
}

// getArgsEnvNspawn returns the nspawn args and env according to the usr used
func getArgsEnvNspawn(p *Pod) ([]string, []string, error) {
	args := []string{}
	env := os.Environ()

	args = append(args, filepath.Join(common.Stage1RootfsPath(p.Root), nspawnBin))
	args = append(args, "--boot") // Launch systemd in the pod
	out, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	lfd, err := common.GetRktLockFD()
	if err != nil {
		return nil, nil, err
	}
	args = append(args, fmt.Sprintf("--pid-file=%v", filepath.Join(out, "pid")))
	args = append(args, fmt.Sprintf("--keep-fd=%v", lfd))
	args = append(args, fmt.Sprintf("--register=true"))

	if !debug {
		args = append(args, "--quiet") // silence most nspawn output (log_warning is currently not covered by this)
	}

	nsargs, err := p.PodToNspawnArgs()
	if err != nil {
		return nil, nil, fmt.Errorf("Failed to generate nspawn args: %v", err)
	}
	args = append(args, nsargs...)

	args = append(args, "--")
	args = append(args, "--default-standard-output=tty")

	if !debug {
		args = append(args, "--log-target=null")
		args = append(args, "--show-status=0")
	}

	return args, env, nil
}

func getArgsEnvKvm(p *Pod) ([]string, []string, error) {
	args := []string{}
	kargs := []string{}
	env := os.Environ()

	args = append(args, filepath.Join(common.Stage1RootfsPath(p.Root), lkvmBin))
	args = append(args, "run")

	args = append(args, "-m 1024")
	args = append(args, "-c 6")

	args = append(args, fmt.Sprintf("--kernel=%v", filepath.Join(common.Stage1RootfsPath(p.Root), bzImg)))
	args = append(args, "--console=virtio")
	kargs = append(kargs, "console=hvc0")

	kargs = append(kargs, "init=/usr/lib/systemd/systemd")

	nsargs, err := p.PodToKvmArgs()
	if err != nil {
		return nil, nil, fmt.Errorf("Failed to generate kvm args: %v", err)
	}
	args = append(args, nsargs...)

	// Arguments to systemd
	kargs = append(kargs, "systemd.default_standard_output=tty")
	if !debug {
		kargs = append(kargs, "systemd.log_target=null")
		kargs = append(kargs, "systemd.show-status=0")
		kargs = append(kargs, "quiet") // silence most nspawn output (log_warning is currently not covered by this)
	}

	args = append(args, "--param")
	args = append(args, strings.Join(kargs, " "))

	return args, env, nil
}

func getArgsEnv(p *Pod) ([]string, []string, error) {
	switch virtualisation {
	case "nspawn":
		return getArgsEnvNspawn(p)
	case "kvm":
		return getArgsEnvKvm(p)
	default:
		return nil, nil, fmt.Errorf("unrecognized containment type: %v", virtualisation)
	}
}

func withClearedCloExec(lfd int, f func() error) error {
	err := sys.CloseOnExec(lfd, false)
	if err != nil {
		return err
	}
	defer sys.CloseOnExec(lfd, true)

	return f()
}

func forwardedPorts(pod *Pod) ([]networking.ForwardedPort, error) {
	fps := []networking.ForwardedPort{}

	for _, ep := range pod.Manifest.Ports {
		n := ""
		fp := networking.ForwardedPort{}

		for an, a := range pod.Apps {
			for _, p := range a.App.Ports {
				if p.Name == ep.Name {
					if n == "" {
						fp.Protocol = p.Protocol
						fp.HostPort = ep.HostPort
						fp.PodPort = p.Port
						n = an
					} else {
						return nil, fmt.Errorf("Ambiguous exposed port in PodManifest: %q and %q both define port %q", n, an, p.Name)
					}
				}
			}
		}

		if n == "" {
			return nil, fmt.Errorf("Port name %q is not defined by any apps", ep.Name)
		}

		fps = append(fps, fp)
	}

	// TODO(eyakubovich): validate that there're no conflicts

	return fps, nil
}

func stage1() int {
	uuid, err := types.NewUUID(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "UUID is missing or malformed")
		return 1
	}

	root := "."
	p, err := LoadPod(root, uuid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load pod: %v\n", err)
		return 1
	}

	// set close-on-exec flag on RKT_LOCK_FD so it gets correctly closed when invoking
	// network plugins
	lfd, err := common.GetRktLockFD()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get rkt lock fd: %v\n", err)
		return 1
	}

	if err := sys.CloseOnExec(lfd, true); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to set FD_CLOEXEC on rkt lock: %v\n", err)
		return 1
	}

	mirrorLocalZoneInfo(p.Root)

	if privNet {
		fps, err := forwardedPorts(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 6
		}

		n, err := networking.Setup(root, p.UUID, fps)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to setup network: %v\n", err)
			return 6
		}
		defer n.Teardown()

		if err = n.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to save networking state %v\n", err)
			return 6
		}

		p.MetadataServiceURL = common.MetadataServicePublicURL(n.GetDefaultHostIP())

		if err = registerPod(p, n.GetDefaultIP()); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to register pod: %v\n", err)
			return 6
		}
		defer unregisterPod(p)
	}

	if err = p.PodToSystemd(interactive, virtualisation); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to configure systemd: %v\n", err)
		return 2
	}

	args, env, err := getArgsEnv(p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get execution parameters: %v\n", err)
		return 3
	}

	var execFn func() error

	if privNet {
		cmd := exec.Cmd{
			Path:   args[0],
			Args:   args,
			Stdin:  os.Stdin,
			Stdout: os.Stdout,
			Stderr: os.Stderr,
			Env:    env,
		}
		execFn = cmd.Run
	} else {
		execFn = func() error {
			return syscall.Exec(args[0], args, env)
		}
	}

	err = withClearedCloExec(lfd, execFn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to execute containment: %v\n", err)
		return 5
	}

	return 0
}

func main() {
	flag.Parse()

	if !debug {
		log.SetOutput(ioutil.Discard)
	}

	// move code into stage1() helper so defered fns get run
	os.Exit(stage1())
}

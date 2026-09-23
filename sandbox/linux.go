// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package sandbox

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
)

const (
	capSetpcap  = 8
	capSysAdmin = 21

	prCapbsetDrop        = 24
	prSetSecurebits      = 28
	prSetNoNewPrivs      = 38
	prCapAmbient         = 47
	prCapAmbientClearAll = 4

	// Root is not special across an exec - no capabilities for uid 0 -
	// and nothing can raise an ambient capability again; both locked.
	secbitNoroot                = 1 << 0
	secbitNorootLocked          = 1 << 1
	secbitNoCapAmbientRaise     = 1 << 6
	secbitNoCapAmbientRaiseLock = 1 << 7
)

// sysProcAttr starts the shim in a user namespace of its own, with its own
// mount namespace. The uid and gid map to themselves, so the task runs as
// the uid it would have run as and what it writes into its checkout belongs
// to the job, as before. CAP_SYS_ADMIN - which the first process of a new
// user namespace holds there - is carried across the shim's exec as an
// ambient capability, because the mounts happen after it, and CAP_SETPCAP
// with it, because giving up the rest takes that one; the shim drops both
// again before the task starts.
func sysProcAttr() (*syscall.SysProcAttr, error) {
	uid, gid := os.Getuid(), os.Getgid()
	return &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}},
		GidMappingsEnableSetgroups: false,
		AmbientCaps:                []uintptr{capSysAdmin, capSetpcap},
	}, nil
}

// shim builds the task's view of the filesystem, gives up every privilege
// and becomes the task - or, for a check, becomes the check, through the
// same exec a task goes through. Everything happens on one locked thread:
// securebits, the bounding set and no_new_privs belong to a thread, and the
// exec takes the calling thread's.
func shim(sp spec, argv []string) (int, error) {
	if sp.Inner {
		// The check, after the exec: already inside, already without
		// privileges - exactly where a task starts.
		return runCheck(*sp.Check), nil
	}
	runtime.LockOSThread()

	// The check re-enters this binary after the exec. Its path may lie
	// under a hidden directory (update:pinup builds pinup into /tmp), so it
	// is opened now and executed through the descriptor.
	var self *os.File
	if sp.Check != nil {
		f, err := os.Open("/proc/self/exe")
		if err != nil {
			return 125, fmt.Errorf("own binary: %w", err)
		}
		self = f
	}

	// Nothing mounted here may reach the job's own namespace.
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return 125, fmt.Errorf("private mounts: %w", err)
	}
	// Every source is opened before the first mount: once the temporary
	// directory is covered, the stand-ins made under it and the checkout
	// inside it are reachable only through these descriptors. They close
	// on exec.
	src := make([]*os.File, len(sp.Ops))
	for i, o := range sp.Ops {
		f, err := os.Open(o.Src)
		if err != nil {
			return 125, fmt.Errorf("%s: %w", o.Path, err)
		}
		src[i] = f
	}
	for i, o := range sp.Ops {
		flags := uintptr(syscall.MS_BIND)
		if o.Keep {
			// The path now lies inside a stand-in; give the original a
			// place to be mounted.
			if err := os.MkdirAll(o.Path, 0o700); err != nil {
				return 125, fmt.Errorf("%s: %w", o.Path, err)
			}
			flags |= syscall.MS_REC
		} else if _, err := os.Lstat(o.Path); err != nil {
			continue // already inside an earlier stand-in
		}
		if err := syscall.Mount(fmt.Sprintf("/proc/self/fd/%d", src[i].Fd()), o.Path, "", flags, ""); err != nil {
			return 125, fmt.Errorf("%s: %w", o.Path, err)
		}
	}

	if err := dropPrivileges(); err != nil {
		return 125, err
	}

	if sp.Check != nil {
		inner, err := json.Marshal(spec{Check: sp.Check, Inner: true})
		if err != nil {
			return 125, err
		}
		err = syscall.Exec(fmt.Sprintf("/proc/self/fd/%d", self.Fd()), []string{"pinup-sandbox-check", Arg, string(inner)}, os.Environ())
		return 126, fmt.Errorf("check: %w", err)
	}
	err := syscall.Exec(argv[0], argv, os.Environ())
	return 126, fmt.Errorf("%s: %w", argv[0], err)
}

// dropPrivileges arranges that the exec to come leaves the task with no
// capability at all, whatever its uid, and none to gain. The capabilities
// the shim still holds are shed by that exec itself - so everything here is
// prctl, none of it a raw capability set handed to the kernel by pointer.
//
//   - no_new_privs: no setuid bit and no file capability can hand anything
//     back to the task.
//   - securebits: uid 0 is not special across the exec - running pinup as
//     root in a container must not give the task root's capabilities - and
//     the ambient set cannot be raised again; both locked.
//   - the bounding set, emptied: nothing the exec could grant is left to
//     grant.
//   - the ambient set, cleared: the one set that survives an exec for a
//     non-root uid, and where the shim's CAP_SYS_ADMIN came from.
func dropPrivileges() error {
	if err := prctl(prSetNoNewPrivs, 1); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	if err := prctl(prSetSecurebits, secbitNoroot|secbitNorootLocked|secbitNoCapAmbientRaise|secbitNoCapAmbientRaiseLock); err != nil {
		return fmt.Errorf("securebits: %w", err)
	}
	for c := uintptr(0); ; c++ {
		err := prctl(prCapbsetDrop, c)
		if errors.Is(err, syscall.EINVAL) {
			break // past the last capability this kernel knows
		}
		if err != nil {
			return fmt.Errorf("bounding set: %w", err)
		}
	}
	if err := prctl(prCapAmbient, prCapAmbientClearAll); err != nil {
		return fmt.Errorf("ambient capabilities: %w", err)
	}
	return nil
}

func prctl(option, arg uintptr) error {
	if _, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, option, arg, 0, 0, 0, 0); e != 0 {
		return e
	}
	return nil
}

// runCheck prints the open doors, one per line; the exit status says
// whether there was any.
func runCheck(c check) int {
	open := openDoors(c)
	for _, line := range open {
		fmt.Println(line)
	}
	if len(open) > 0 {
		return 1
	}
	return 0
}

// openDoors tries what a task must not manage and reports each success.
func openDoors(c check) []string {
	var open []string
	if _, err := os.ReadFile(c.Environ); err == nil {
		open = append(open, "open: the parent's environment is readable")
	}
	if b, err := os.ReadFile(c.Visible); err != nil || string(b) != c.Want {
		open = append(open, fmt.Sprintf("broken: a kept path is not visible (%v)", err))
	}
	for _, p := range c.Absent {
		if _, err := os.Lstat(p); err == nil {
			open = append(open, "open: "+p+" is visible")
		}
	}
	for _, p := range c.Empty {
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
			open = append(open, "open: "+p+" is readable")
		}
	}
	// thread-self: capabilities and no_new_privs belong to a thread, and
	// /proc/self/status shows the process's first one. After the exec every
	// thread starts from the same credentials, so the two agree - but the
	// first version of this check ran before an exec, read /proc/self, and
	// failed now and then under load for a door that was closed (stress
	// run, 2026-09-23). The thread asking is the one to read.
	status, err := os.ReadFile("/proc/thread-self/status")
	if err != nil {
		open = append(open, "broken: /proc/thread-self/status: "+err.Error())
	}
	for line := range strings.Lines(string(status)) {
		k, v, _ := strings.Cut(strings.TrimSpace(line), ":")
		v = strings.TrimSpace(v)
		switch {
		case (k == "CapEff" || k == "CapPrm" || k == "CapBnd" || k == "CapAmb") && strings.Trim(v, "0") != "":
			open = append(open, "open: capabilities kept: "+k+"="+v)
		case k == "NoNewPrivs" && v != "1":
			open = append(open, "open: no_new_privs is not set")
		}
	}
	return open
}

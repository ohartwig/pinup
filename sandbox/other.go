// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package sandbox

import "syscall"

// Off Linux there is no user namespace to build on. Saying so is the whole
// job of this file: a platform that cannot isolate a task must not run it
// as if it had.
func sysProcAttr() (*syscall.SysProcAttr, error) { return nil, ErrUnsupported }

func shim(spec, []string) (int, error) { return 125, ErrUnsupported }

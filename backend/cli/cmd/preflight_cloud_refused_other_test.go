// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package cmd

import "syscall"

var (
	refusedOnThisOS    = []syscall.Errno{syscall.ECONNREFUSED}
	notRefusedOnThisOS = []syscall.Errno{syscall.ECONNRESET, syscall.ENETUNREACH}
)

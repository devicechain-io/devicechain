// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package test

import "errors"

// refusingAddr is implemented only on unix (jscluster_refuser_unix.go). Elsewhere a
// cluster fixture fails to start rather than advertise an address it does not hold.
type refusingAddr struct{ addr string }

func holdRefusingAddr() (*refusingAddr, error) {
	return nil, errors.New("holding a refusing route address is implemented only on unix")
}

func (r *refusingAddr) close() {}

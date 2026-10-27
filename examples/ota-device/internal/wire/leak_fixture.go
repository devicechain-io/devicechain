// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build otaleak

package wire

// This file exists only to be the negative control of TestDeviceImportsNothingFromThePlatform. It
// is excluded from every real build; the test builds with -tags otaleak to prove that the guard
// goes red when a platform package leaks into the device.
import _ "github.com/devicechain-io/dc-microservice/ota"

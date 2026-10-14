// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import "github.com/devicechain-io/dc-device-management/model"

// DeviceImportResultResolver serves the outcomes of an importDevices call.
type DeviceImportResultResolver struct {
	M *model.DeviceImportResult
}

func (r *DeviceImportResultResolver) DryRun() bool    { return r.M.DryRun }
func (r *DeviceImportResultResolver) Created() int32  { return r.M.Created }
func (r *DeviceImportResultResolver) Exists() int32   { return r.M.Exists }
func (r *DeviceImportResultResolver) Conflict() int32 { return r.M.Conflict }
func (r *DeviceImportResultResolver) Invalid() int32  { return r.M.Invalid }
func (r *DeviceImportResultResolver) Refused() int32  { return r.M.Refused }

func (r *DeviceImportResultResolver) Rows() []*DeviceImportRowResultResolver {
	out := make([]*DeviceImportRowResultResolver, 0, len(r.M.Rows))
	for _, row := range r.M.Rows {
		out = append(out, &DeviceImportRowResultResolver{M: row})
	}
	return out
}

// DeviceImportRowResultResolver serves one row's outcome. It exposes no field that could
// carry a credential secret: the model never puts one in an outcome.
type DeviceImportRowResultResolver struct {
	M *model.DeviceImportRowResult
}

func (r *DeviceImportRowResultResolver) Row() int32       { return r.M.Row }
func (r *DeviceImportRowResultResolver) Token() string    { return r.M.Token }
func (r *DeviceImportRowResultResolver) Outcome() string  { return r.M.Outcome }
func (r *DeviceImportRowResultResolver) Code() *string    { return r.M.Code }
func (r *DeviceImportRowResultResolver) Message() *string { return r.M.Message }

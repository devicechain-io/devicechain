// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"context"
	"fmt"
	"time"
)

const queryCalculatedJob = `query($token:String!){calculatedMetricBackfill(token:$token){token status profileToken from to samples written}}`

func (r *Reader) CalculatedJob(ctx context.Context, token string) (CalculatedJob, error) {
	var out struct {
		CalculatedMetricBackfill *struct {
			Token, Status, ProfileToken, From, To string
			Samples, Written                      float64
		}
	}
	if err := r.session.Query(ctx, r.events, queryCalculatedJob, map[string]any{"token": token}, &out); err != nil {
		return CalculatedJob{}, err
	}
	j := out.CalculatedMetricBackfill
	if j == nil {
		return CalculatedJob{}, fmt.Errorf("backfill %s not found", token)
	}
	from, err := time.Parse(time.RFC3339Nano, j.From)
	if err != nil {
		return CalculatedJob{}, err
	}
	to, err := time.Parse(time.RFC3339Nano, j.To)
	if err != nil {
		return CalculatedJob{}, err
	}
	return CalculatedJob{j.Token, j.Status, j.ProfileToken, from, to, j.Samples, j.Written}, nil
}

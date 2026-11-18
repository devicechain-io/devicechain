// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/devicechain-io/dc-notification-management/model"
)

type positionSeams interface {
	Seam(config.ServiceEndpoint, ...auth.Authority) (svcclient.Seam, error)
}

// NewPositionOracle owns a separate seam from the orphan sweep. The absence of
// location:read is the point: DM's withholding is the authoritative verdict.
func NewPositionOracle(peers positionSeams, endpoint config.ServiceEndpoint) (model.PositionOracle, error) {
	seam, err := peers.Seam(endpoint, auth.AlarmRead)
	if err != nil {
		return nil, err
	}
	return &positionOracle{client: seam.Client, url: seam.URL}, nil
}

type positionOracle struct {
	client *svcclient.Client
	url    string
}

func (s *positionOracle) VisibleWithoutLocation(ctx context.Context, tokens []string) (map[string]bool, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, core.ErrNoTenant
	}
	var out struct {
		AlarmsByToken *[]struct{ Token string } `json:"alarmsByToken"`
	}
	err := s.client.Query(ctx, s.url, tenant,
		`query($tokens:[String!]!){ alarmsByToken(tokens:$tokens){ token } }`,
		map[string]any{"tokens": tokens}, &out)
	if err != nil {
		return nil, err
	}
	if out.AlarmsByToken == nil {
		return nil, fmt.Errorf("device-management returned no position visibility result")
	}
	visible := map[string]bool{}
	for _, alarm := range *out.AlarmsByToken {
		if alarm.Token == "" {
			return nil, fmt.Errorf("invalid position visibility result")
		}
		visible[alarm.Token] = true
	}
	return visible, nil
}

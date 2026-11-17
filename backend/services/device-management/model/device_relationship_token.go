// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var relationshipTokenNamespace = uuid.MustParse("6f1c3a52-8d0e-5b7a-9c41-2e5d7a9b1c30")

// DeviceAuthoredRelationshipToken derives the stable event-created edge identity.
// Its namespace, field order and NUL separators are persisted idempotency data.
func DeviceAuthoredRelationshipToken(tenant, device, targetType, target, relationshipType string) string {
	return uuid.NewSHA1(relationshipTokenNamespace, []byte(strings.Join([]string{
		tenant, "device", device, targetType, target, relationshipType}, "\x00"))).String()
}

// ErrDeviceAuthoredEdgesBlockTracking refuses promotion of device-created edges to ownership.
// Count covers every match; Edges holds at most five descriptions in edge-id order.
type ErrDeviceAuthoredEdgesBlockTracking struct {
	Count int
	Edges []string
}

func (e *ErrDeviceAuthoredEdgesBlockTracking) Error() string {
	return fmt.Sprintf("%d device-authored relationships block tracking: %s; delete those relationships, then mark the type tracked", e.Count, strings.Join(e.Edges, "; "))
}

// Unwrap uses the same existing-data refusal class as deleting a type in use.
func (e *ErrDeviceAuthoredEdgesBlockTracking) Unwrap() error {
	return integrity.NewRefusal(integrity.ClassReference, e.Error())
}

const deviceAuthoredEdgePageSize = 128

// The caller holds the type's UPDATE lock until the scan and flip commit. Device
// creates hold SHARE on that same row through insertion, so neither can pass the
// other between the Tracked read and its write (Postgres READ COMMITTED).
func refuseTrackingDeviceAuthoredEdges(tx *gorm.DB, typ *EntityRelationshipType) error {
	tenant, _ := core.TenantFromContext(tx.Statement.Context)
	refusal := &ErrDeviceAuthoredEdgesBlockTracking{}
	var after uint
	for {
		var edges []EntityRelationship
		if err := tx.Where("relationship_type_id = ? AND source_type = ? AND id > ?", typ.ID, "device", after).
			Order("id ASC").Limit(deviceAuthoredEdgePageSize).Find(&edges).Error; err != nil {
			return err
		}
		if len(edges) == 0 {
			break
		}
		ids := make([]uint, 0, len(edges))
		for _, edge := range edges {
			ids = append(ids, edge.SourceId)
		}
		var devices []Device
		if err := tx.Select("id", "token").Where("id IN ?", ids).Find(&devices).Error; err != nil {
			return err
		}
		tokens := make(map[uint]string, len(devices))
		for _, device := range devices {
			tokens[device.ID] = device.Token
		}
		for _, edge := range edges {
			device, ok := tokens[edge.SourceId]
			if !ok {
				return fmt.Errorf("relationship %q source device: %w", edge.Token, gorm.ErrRecordNotFound)
			}
			if edge.Token == DeviceAuthoredRelationshipToken(tenant, device, edge.TargetType, edge.TargetToken, typ.Token) {
				refusal.Count++
				if len(refusal.Edges) < 5 {
					refusal.Edges = append(refusal.Edges, fmt.Sprintf("device %s -> %s %s", device, edge.TargetType, edge.TargetToken))
				}
			}
		}
		after = edges[len(edges)-1].ID
	}
	if refusal.Count > 0 {
		return refusal
	}
	return nil
}

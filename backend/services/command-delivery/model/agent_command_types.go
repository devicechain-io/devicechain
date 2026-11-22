// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"github.com/devicechain-io/dc-microservice/actiongate"
	"time"
)

const LedgerScope = "command-delivery"

// Proposed platform defaults; tenant policy can only narrow the client/principal
// submission limits through actionsPerHour. Execution decisions currently omit it.
const (
	TenantOpenProposals           = 100
	TenantProposalsPerHour        = 1000
	ExecutionsPerHourPerClient    = 50
	ExecutionsPerHourPerPrincipal = 50
	TenantExecutionsPerHour       = 200
	MaxProposalRequestId          = 128
	MaxProposalRationale          = 2048
	MaxProposalRejectNote         = 512
	ProposalStatusDryRun          = "DRY_RUN"
	ProposalStatusPending         = "PENDING_APPROVAL"
	ProposalStatusRefused         = "REFUSED"
	ProposalStatusExisting        = "EXISTING"
)

// ProposalTargetReader must use the initiating caller's bearer for every read.
// Errors mean validation could not be decided; they never mean a rejected payload.
type ProposalTargetReader interface {
	ReadProposalTarget(context.Context, string, string, []byte, time.Time) (*ProposalTarget, error)
}
type ProposalTarget struct {
	Exists                   bool
	Incarnation              uint64
	Constrained              bool
	Found                    bool
	Impact                   string
	DefinitionFingerprint    string
	Allowed                  bool
	RejectCode, RejectReason string
	Anchors                  []string
	Hold                     *DispatchHold
	ScheduleRefusal          *BatchDeviceRefusal
}
type CommandProposalRequest struct {
	DeviceToken, Name                  string
	Payload                            *string
	RequestId                          string
	Rationale, ExpiresAt, DeliveryMode *string
	MaxAttempts                        *int32
	DryRun                             bool
}
type CommandProposalPreview struct {
	DeviceToken        string
	Incarnation        uint64
	Command            string
	Payload            []byte
	PayloadPresent     bool
	Impact, Definition string
	Cutoff             time.Time
	Hold               *DispatchHold
}
type CommandProposalOutcome struct {
	Status            string
	Proposal          *AgentProposal
	Verdict           actiongate.Verdict
	RequiredApprovals int
	Reasons           []string
	Digest            string
	ExpiresAt         *time.Time
	TargetCount       int
	Preview           *CommandProposalPreview
}

func (api *Api) agentNow() time.Time {
	if api.proposalNow != nil {
		return api.proposalNow().UTC()
	}
	return time.Now().UTC()
}

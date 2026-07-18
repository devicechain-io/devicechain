// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"

	"github.com/devicechain-io/dc-device-management/model"
)

// CommandEnqueueVerdictResolver exposes the enqueue gate's verdict.
type CommandEnqueueVerdictResolver struct {
	M model.CommandEnqueueVerdict
}

// Whether the command may be enqueued.
func (r *CommandEnqueueVerdictResolver) Allowed() bool {
	return r.M.Allowed
}

// Why the command was rejected; null when allowed.
func (r *CommandEnqueueVerdictResolver) Reason() *string {
	if r.M.Reason == "" {
		return nil
	}
	reason := r.M.Reason
	return &reason
}

// ValidateCommandEnqueue decides whether a command may be enqueued to a device
// (ADR-043 decision 3). It is gated on device:read — the same authority the
// existing command/profile/device reads use, and the one command-delivery's
// service client already requests, so no new authority is introduced.
//
// A rejection returns allowed=false with a reason rather than a GraphQL error:
// the caller (command-delivery's enqueue path) must distinguish "this command is
// invalid" — which it relays to its API client — from "the check failed" — on
// which it fails closed with a sanitized message. Collapsing both into an error
// would make an outage indistinguishable from a bad command, and an outage would
// then read to the client as "your command is invalid".
func (r *SchemaResolver) ValidateCommandEnqueue(ctx context.Context, args struct {
	DeviceToken string
	CommandKey  string
	Payload     *string
}) (*CommandEnqueueVerdictResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	var payload []byte
	if args.Payload != nil {
		payload = []byte(*args.Payload)
	}

	verdict, err := r.GetApi(ctx).ValidateCommandEnqueue(ctx, args.DeviceToken, args.CommandKey, payload)
	if err != nil {
		return nil, err
	}
	return &CommandEnqueueVerdictResolver{M: *verdict}, nil
}

// PublishedCommandResolver exposes one command of a device's published vocabulary.
// It carries no id or token on purpose — see the schema comment on PublishedCommand.
type PublishedCommandResolver struct {
	M model.CommandDefinition
}

func (r *PublishedCommandResolver) CommandKey() string { return r.M.CommandKey }

func (r *PublishedCommandResolver) Name() *string { return util.NullStr(r.M.Name) }

func (r *PublishedCommandResolver) Description() *string { return util.NullStr(r.M.Description) }

func (r *PublishedCommandResolver) ParameterSchema() *string {
	return util.MetadataStr(r.M.ParameterSchema)
}

// DeviceCommandVocabularyResolver exposes the commands a device currently accepts.
type DeviceCommandVocabularyResolver struct {
	M model.CommandVocabulary
}

func (r *DeviceCommandVocabularyResolver) Constrained() bool { return r.M.Constrained }

func (r *DeviceCommandVocabularyResolver) Commands() []*PublishedCommandResolver {
	result := make([]*PublishedCommandResolver, 0)
	for _, cd := range r.M.Commands {
		if cd == nil {
			continue
		}
		result = append(result, &PublishedCommandResolver{M: *cd})
	}
	return result
}

// DeviceCommandVocabulary lists the commands a device currently accepts (ADR-043
// decision 3) — the listing counterpart of the ValidateCommandEnqueue gate above.
// Both read the same resolution, so the vocabulary the console offers is the one the
// gate accepts.
//
// Gated on device:read, the same authority as the gate and the other command reads —
// knowing what a device accepts is no more privileged than reading its profile, which
// already exposes the draft of the same vocabulary.
//
// An unresolvable device token returns null rather than an error. A stale token is a
// state a client legitimately holds — a dashboard widget outlives the device it was
// pointed at — and erroring on a non-null field would null out every sibling result in
// a batched document over one dangling reference.
func (r *SchemaResolver) DeviceCommandVocabulary(ctx context.Context, args struct {
	DeviceToken string
}) (*DeviceCommandVocabularyResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	vocab, err := r.GetApi(ctx).DeviceCommandVocabulary(ctx, args.DeviceToken)
	if err != nil {
		return nil, err
	}
	if !vocab.DeviceExists {
		return nil, nil
	}
	return &DeviceCommandVocabularyResolver{M: *vocab}, nil
}

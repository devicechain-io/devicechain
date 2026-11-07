// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"

	"github.com/devicechain-io/dc-microservice/auth"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-command-delivery/model"
)

// The answers acknowledgeCommandDelivery and respondToPulledCommand give. They are served and
// frozen on release; event-sources turns each into an HTTP status.
const (
	pullRecorded      = "RECORDED"
	pullNotFound      = "NOT_FOUND"
	pullNotPulled     = "NOT_PULLED"
	pullNotAwaiting   = "NOT_AWAITING"
	pullNotAnswerable = "NOT_ANSWERABLE"
)

// PullCommands hands a pull-channel device the commands it may be sent and claims each.
//
// 🔑 IT IS GATED ON command:claim FOR THE REASON markCommandSent IS, AND THE CALLER OWES THE
// AUTHENTICATION. The resolver cannot tell which device is on the other end of an HTTP request;
// event-sources proved the device's access token and names the device it proved. A service token
// holding command:claim is therefore trusted to name only a device it authenticated, exactly as
// the LwM2M drain is trusted to name only a device whose session it holds.
func (r *SchemaResolver) PullCommands(ctx context.Context, args struct {
	DeviceToken string
	Limit       *int32
}) ([]*PulledCommandResolver, error) {
	if err := auth.Authorize(ctx, auth.CommandClaim); err != nil {
		return nil, err
	}
	limit := 0
	if args.Limit != nil {
		limit = int(*args.Limit)
	}
	pulled, err := r.GetApi(ctx).PullCommands(ctx, args.DeviceToken, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*PulledCommandResolver, 0, len(pulled))
	for _, p := range pulled {
		out = append(out, &PulledCommandResolver{M: p})
	}
	return out, nil
}

// AcknowledgeCommandDelivery records a pull-channel device's acknowledgement of a command.
func (r *SchemaResolver) AcknowledgeCommandDelivery(ctx context.Context, args struct {
	Token       string
	DeviceToken string
}) (string, error) {
	if err := auth.Authorize(ctx, auth.CommandClaim); err != nil {
		return "", err
	}
	ok, err := r.GetApi(ctx).AcknowledgeDelivery(ctx, args.Token, args.DeviceToken)
	switch {
	case err == nil && ok:
		return pullRecorded, nil
	case err == nil:
		return pullNotAwaiting, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return pullNotFound, nil
	case errors.Is(err, model.ErrCommandNotPulled):
		return pullNotPulled, nil
	}
	return "", err
}

// RespondToPulledCommand records a pull-channel device's answer to a command.
func (r *SchemaResolver) RespondToPulledCommand(ctx context.Context, args struct {
	Token       string
	DeviceToken string
	Success     bool
	Payload     *string
	Error       *string
}) (string, error) {
	if err := auth.Authorize(ctx, auth.CommandClaim); err != nil {
		return "", err
	}
	_, err := r.GetApi(ctx).RecordPulledResponse(ctx, args.Token, args.DeviceToken, args.Success,
		args.Payload, args.Error)
	switch {
	case err == nil:
		return pullRecorded, nil
	case errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, model.ErrResponderNotCommandOwner):
		return pullNotFound, nil
	case errors.Is(err, model.ErrCommandNotPulled):
		return pullNotPulled, nil
	case errors.Is(err, model.ErrResponseNonceMismatch), errors.Is(err, model.ErrCommandNotAnswerable):
		return pullNotAnswerable, nil
	}
	return "", err
}

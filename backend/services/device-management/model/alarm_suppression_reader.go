// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/entity"
)

// AlarmSuppressionReader belongs to one authorized request/list. It captures a
// window snapshot and current device facts once, including failed fact lookups.
// Callers supply alarm rows already read under the caller's visibility rules.
type AlarmSuppressionReader struct {
	api      *Api
	mu       sync.Mutex
	loaded   bool
	windows  []compiledWindow
	err      error
	at       time.Time
	facts    map[uint]deviceFacts
	failures map[uint]error
}

func (api *Api) NewAlarmSuppressionReader(at time.Time) *AlarmSuppressionReader {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return &AlarmSuppressionReader{api: api, at: at, facts: map[uint]deviceFacts{}, failures: map[uint]error{}}
}

func (r *AlarmSuppressionReader) load(ctx context.Context) error {
	if !r.loaded {
		r.loaded = true
		if r.at.Before(time.Now().Add(-10 * time.Minute)) {
			r.windows, r.err = loadSuppressionWindows(r.api.RDB.DB(suppressionFactContext(ctx)).Scopes(suppressionLiveAt(r.at)).
				Where("start_time <= ? OR (kind = ? AND (valid_from IS NULL OR valid_from <= ?))", r.at, SuppressionRecurring, r.at))
		} else {
			r.windows, r.err = r.api.suppressionWindows(ctx)
		}
	}
	return r.err
}

func (r *AlarmSuppressionReader) Match(ctx context.Context, alarm *Alarm) (*SuppressionMatch, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.AlarmRead); err != nil {
		return nil, err
	}
	if alarm == nil || alarm.State != string(AlarmStateActive) || alarm.OriginatorType != string(entity.TypeDevice) {
		return nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.load(ctx); err != nil {
		return nil, err
	}
	if len(r.windows) == 0 {
		return nil, nil
	}
	id := alarm.OriginatorId
	facts, ok := r.facts[id]
	if !ok {
		var device Device
		err := r.api.RDB.DB(suppressionFactContext(ctx)).First(&device, id).Error
		if err == nil {
			facts, err = r.api.suppressionDeviceFacts(suppressionFactContext(ctx), &device, r.windows)
		}
		r.facts[id], r.failures[id] = facts, err
	}
	if err := r.failures[id]; err != nil {
		return nil, err
	}
	return matchWindows(r.windows, facts, alarm.AlarmKey, alarm.Severity, r.at), nil
}

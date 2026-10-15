// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package recording makes a board recording (the file board-replay plays) of a sitepulse
// run whose device plane this simulator played (--play-devices).
//
// A recording is made in two steps, because the platform keeps two kinds of history
// differently:
//
//   - WATCH, during the run: the alarm list is read on a fixed cadence and every answer is
//     appended to a file. The platform keeps one alarm object per (device, rule) and
//     rewrites its raise and clear times on each cycle, so the alarm history of a run
//     exists ONLY if somebody watched it; it cannot be read back afterwards.
//   - BUILD, after it: the measurements and positions of a window are read back from the
//     event store, every row is checked against the device plane's publish log (a row the
//     devices never sent is refused, as a fabricated one), and the board recording is
//     written with the watched alarm history of the same window.
package recording

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
)

// WatchedAlarm is one alarm as the platform's alarm list answered it.
type WatchedAlarm struct {
	Token        string  `json:"token"`
	Originator   string  `json:"originatorToken"`
	AlarmKey     string  `json:"alarmKey"`
	MetricKey    string  `json:"metricKey"`
	State        string  `json:"state"`
	Severity     string  `json:"severity"`
	RaisedTime   *string `json:"raisedTime"`
	ClearedTime  *string `json:"clearedTime"`
	Acknowledged bool    `json:"acknowledged"`
}

// AlarmWatch is one answer of the alarm list: when the query was sent, when it was
// answered, and the WHOLE list (every alarm, in every state).
type AlarmWatch struct {
	RequestedAt string         `json:"requestedAt"`
	AnsweredAt  string         `json:"answeredAt"`
	Total       int            `json:"total"`
	Alarms      []WatchedAlarm `json:"alarms"`
}

// watchPageSize bounds one read of the alarm list. A sitepulse site holds at most one
// alarm per machine and rule (19 x 3); an answer with more than this is not cut, it is
// refused (see Watch).
const watchPageSize = 500

const alarmsQuery = `query($c: AlarmSearchCriteria!) {
  alarms(criteria: $c) {
    results { token originatorToken alarmKey metricKey state severity raisedTime clearedTime acknowledged }
    pagination { totalRecords }
  }
}`

// Watch reads the tenant's whole alarm list every `every` until ctx ends, appending each
// answer to out as one JSON line. An answer that lists fewer alarms than its total is a
// truncated one and is NOT written: a recording folds each answer as the whole set, and a
// truncated one would erase every alarm it left out. A failed read is reported to warn
// and the watch goes on (the gap shows as a longer interval between two answers); a
// failed WRITE ends it, since every answer after it would be lost.
func Watch(ctx context.Context, session *userclient.TenantSession, deviceMgmtURL string, every time.Duration,
	out io.Writer, warn func(error)) error {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		line, err := watchOnce(ctx, session, deviceMgmtURL)
		switch {
		case err != nil && ctx.Err() != nil:
			return nil
		case err != nil:
			warn(err)
		default:
			if _, err := out.Write(append(line, '\n')); err != nil {
				return fmt.Errorf("write the alarm watch: %w", err)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func watchOnce(ctx context.Context, session *userclient.TenantSession, deviceMgmtURL string) ([]byte, error) {
	requested := time.Now().UTC()
	var res struct {
		Alarms struct {
			Results    []WatchedAlarm `json:"results"`
			Pagination struct {
				TotalRecords int `json:"totalRecords"`
			} `json:"pagination"`
		} `json:"alarms"`
	}
	vars := map[string]any{"c": map[string]any{"pageNumber": 1, "pageSize": watchPageSize}}
	if err := session.Query(ctx, deviceMgmtURL, alarmsQuery, vars, &res); err != nil {
		return nil, fmt.Errorf("read the alarm list: %w", err)
	}
	answered := time.Now().UTC()
	if n, total := len(res.Alarms.Results), res.Alarms.Pagination.TotalRecords; n != total {
		return nil, fmt.Errorf("the alarm list answered %d of %d alarms; a truncated answer is not recorded", n, total)
	}
	return json.Marshal(AlarmWatch{
		RequestedAt: requested.Format(time.RFC3339Nano),
		AnsweredAt:  answered.Format(time.RFC3339Nano),
		Total:       res.Alarms.Pagination.TotalRecords,
		Alarms:      res.Alarms.Results,
	})
}

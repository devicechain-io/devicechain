// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package actiongate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// CommitPointIdleBound is X: the longest the execution transaction may sit idle between the
// commit-point decision being asked and COMMIT. CommitPointDeadline is the decision RPC's deadline,
// strictly below X so a decision that answers in time still leaves the commit inside the bound.
const (
	CommitPointIdleBound = 5 * time.Second
	CommitPointDeadline  = 4 * time.Second
)

// commitPointIdleSQL installs CommitPointIdleBound. It is a literal so the raw-SQL guard can
// read it; TestCommitPointBounds keeps it equal to the constant.
const commitPointIdleSQL = "SET LOCAL idle_in_transaction_session_timeout = '5000ms'"

// ErrCommitPointSetup distinguishes failure to install the idle bound from a decision RPC error.
var ErrCommitPointSetup = errors.New("commit point setup failed")

var commitPointNow = time.Now

// CommitPoint asks decide as the LAST act of an effect transaction before COMMIT.
// An answer that arrives after the deadline is not trusted, whatever the decider did
// with the context.
//
// THE TAIL RULE: idle_in_transaction_session_timeout bounds ONE idle interval and every
// statement resets it. On Postgres the bound holds only because the caller runs no SQL
// between CommitPoint's return and COMMIT, only in-memory checks. A refusal or error must
// roll the whole transaction back and record any refusal in a separate transaction.
// The residual: an authorization change committed in user-management after its snapshot
// and before this COMMIT can be overtaken, by at most X plus COMMIT processing.
func CommitPoint(ctx context.Context, tx *gorm.DB, decide func(context.Context) (ExecutionDecision, error)) (ExecutionDecision, error) {
	if tx.Dialector.Name() == "postgres" {
		if err := tx.Exec(commitPointIdleSQL).Error; err != nil {
			return ExecutionDecision{}, fmt.Errorf("%w: setting idle transaction timeout: %w", ErrCommitPointSetup, err)
		}
	}
	dctx, cancel := context.WithTimeout(ctx, CommitPointDeadline)
	defer cancel()
	decision, err := decide(dctx)
	// 🔴 A context timer can fire late; compare monotonic arrival time as well.
	deadline, _ := dctx.Deadline()
	if err == nil && (dctx.Err() != nil || !commitPointNow().Before(deadline)) {
		cause := dctx.Err()
		if cause == nil {
			cause = context.DeadlineExceeded
		}
		return ExecutionDecision{}, fmt.Errorf("commit-point decision answered after its deadline: %w", cause)
	}
	return decision, err
}

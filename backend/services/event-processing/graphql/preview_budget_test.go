// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"
	"time"
)

func TestPreviewBudgetStaysBelowTheRequestDeadline(t *testing.T) {
	if got := previewBudget(context.Background()); got != previewTimeBudget {
		t.Fatalf("with no deadline the budget is the default, got %v", got)
	}
	long, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if got := previewBudget(long); got != previewTimeBudget {
		t.Fatalf("a long deadline leaves the default budget, got %v", got)
	}
	short, cancel2 := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel2()
	got := previewBudget(short)
	if got <= 0 || got >= 20*time.Second {
		t.Fatalf("a short deadline must cap the budget below it, got %v", got)
	}
}

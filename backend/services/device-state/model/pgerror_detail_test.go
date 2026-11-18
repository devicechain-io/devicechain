// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// describeDBError preserves the server's diagnostic fields through error wrappers.
func describeDBError(err error) string {
	if err == nil {
		return "<nil>"
	}
	description := err.Error()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return description
	}
	for _, field := range []struct{ label, value string }{
		{"Code", pgErr.Code},
		{"Severity", pgErr.Severity},
		{"Message", pgErr.Message},
		{"Detail", pgErr.Detail},
		{"Hint", pgErr.Hint},
		{"Where", pgErr.Where},
		{"SchemaName", pgErr.SchemaName},
		{"TableName", pgErr.TableName},
		{"ConstraintName", pgErr.ConstraintName},
		{"InternalQuery", pgErr.InternalQuery},
	} {
		if field.value != "" {
			description += "\n" + field.label + ": " + field.value
		}
	}
	return description
}

func TestDescribeDBError(t *testing.T) {
	pgErr := &pgconn.PgError{
		Code:    "40P01",
		Message: "deadlock detected",
		Detail: "Process 11 waits for ShareLock on transaction 22; blocked by process 33.\n" +
			"Process 33 waits for ShareLock on transaction 44; blocked by process 11.",
		Where: `while updating tuple (0,5) in relation "device_states"`,
	}
	err := fmt.Errorf("outer: %w", &BatchRefusal{Tenant: "tw", Err: pgErr})
	description := describeDBError(err)
	for _, want := range []string{err.Error(), "Code: " + pgErr.Code, "Detail: " + pgErr.Detail, "Where: " + pgErr.Where} {
		if !strings.Contains(description, want) {
			t.Errorf("description %q does not contain %q", description, want)
		}
	}
	if strings.Contains(err.Error(), "blocked by process") {
		t.Errorf("err.Error() unexpectedly includes the server's detail: %s", err)
	}
	plainErr := errors.New("not a PostgreSQL error")
	if got := describeDBError(plainErr); got != plainErr.Error() {
		t.Errorf("describeDBError(non-pg) = %q; want %q", got, plainErr.Error())
	}
	if got := describeDBError(nil); got != "<nil>" {
		t.Errorf("describeDBError(nil) = %q; want <nil>", got)
	}
}

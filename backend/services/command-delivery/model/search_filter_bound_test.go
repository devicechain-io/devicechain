// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
)

func TestCommandSearchFilterBound(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(t.Context(), "A")
	for _, seed := range []struct {
		token  string
		status CommandStatus
	}{
		{"finished", CommandSuccessful},
		{"live", CommandQueued},
	} {
		cmd := &Command{DeviceToken: "d", Name: "reboot", Status: seed.status.String()}
		cmd.Token = seed.token
		if seed.status == CommandSuccessful {
			cmd.Outcome = sql.NullString{String: OutcomeSucceeded.String(), Valid: true}
		}
		require.NoError(t, api.RDB.DB(ctx).Create(cmd).Error)
	}

	// Install after setup, so zero statements means refusal before ANY search SQL.
	var statements []string
	api.RDB.Database.Logger = sqlRecorder{captured: &statements}
	search := func(t *testing.T, criteria CommandSearchCriteria, want ...string) {
		t.Helper()
		statements = nil
		found, err := api.Commands(ctx, criteria)
		require.NoError(t, err)
		require.Len(t, statements, 2, "accepted searches must execute COUNT and data SQL")
		require.Contains(t, strings.ToUpper(statements[0]), "COUNT(")
		tokens := make([]string, 0, len(found.Results))
		for _, cmd := range found.Results {
			tokens = append(tokens, cmd.Token)
		}
		require.ElementsMatch(t, want, tokens)
		require.EqualValues(t, len(want), found.Pagination.TotalRecords)
	}

	for _, field := range []struct {
		name  string
		value string
		set   func(*[]string) CommandSearchCriteria
	}{
		{"statuses", CommandSuccessful.String(), func(v *[]string) CommandSearchCriteria {
			return CommandSearchCriteria{Statuses: v}
		}},
		{"outcomes", OutcomeSucceeded.String(), func(v *[]string) CommandSearchCriteria {
			return CommandSearchCriteria{Outcomes: v}
		}},
	} {
		t.Run(field.name, func(t *testing.T) {
			// Valid duplicates must count toward the request-work ceiling.
			values := make([]string, rdb.MaxLookupKeys+1)
			for i := range values {
				values[i] = field.value
			}
			statements = nil
			found, err := api.Commands(ctx, field.set(&values))
			require.Empty(t, statements, "over-limit input must refuse before COUNT or data SQL")
			require.Nil(t, found)
			le, ok := limit.As(err)
			require.True(t, ok, "expected typed limit refusal, got %v", err)
			require.Equal(t, field.name, le.What)
			require.Equal(t, rdb.MaxLookupKeys+1, le.Got)
			require.Equal(t, rdb.MaxLookupKeys, le.Max)
			require.Equal(t, "LIMIT_EXCEEDED", le.Extensions()["code"])

			one := []string{field.value}
			search(t, field.set(&one), "finished")
			atBound := values[:rdb.MaxLookupKeys]
			search(t, field.set(&atBound), "finished")
			search(t, field.set(nil), "finished", "live")
			empty := []string{}
			search(t, field.set(&empty), "finished", "live")
			var nilSlice []string
			search(t, field.set(&nilSlice), "finished", "live")
			unknown := []string{"UNKNOWN"}
			search(t, field.set(&unknown))
			mixed := []string{"UNKNOWN", field.value}
			search(t, field.set(&mixed), "finished")
		})
	}

	// Status and Statuses remain ANDed, including when the list is at its bound.
	statuses := make([]string, rdb.MaxLookupKeys)
	for i := range statuses {
		statuses[i] = CommandSuccessful.String()
	}
	status := CommandSuccessful.String()
	search(t, CommandSearchCriteria{Status: &status, Statuses: &statuses}, "finished")
	status = CommandQueued.String()
	search(t, CommandSearchCriteria{Status: &status, Statuses: &statuses})
}

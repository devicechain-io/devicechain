// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/devicechain-io/dc-microservice/credbinding"
	gqlerrors "github.com/graph-gophers/graphql-go/errors"
)

func TestCredentialResupplyCodeSurvivesWrap(t *testing.T) {
	err := fmt.Errorf("update: %w", &credbinding.ResupplyRequiredError{Subject: "connector", Token: "sink", Changed: []string{"urls"}})
	qe := &gqlerrors.QueryError{Message: err.Error(), ResolverError: err}
	answerIntegrity([]*gqlerrors.QueryError{qe})
	if qe.Extensions["code"] != credbinding.Code || !reflect.DeepEqual(qe.Extensions["changed"], []string{"urls"}) {
		t.Fatalf("extensions=%v", qe.Extensions)
	}
}

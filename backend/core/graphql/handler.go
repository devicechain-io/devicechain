/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package graphql

import (
	"context"
	"net/http"

	graphql "github.com/graph-gophers/graphql-go"

	"github.com/graph-gophers/graphql-go/relay"
)

type ContextKey string

const (
	ContextRdbKey ContextKey = "rdb"
	ContextApiKey ContextKey = "api"
)

// Adds extra context to http request.
type HttpHandler struct {
	Schema           *graphql.Schema
	Relay            *relay.Handler
	ContextProviders map[ContextKey]interface{}
}

// Create new http handler.
func NewHttpHandler(schema *graphql.Schema, providers map[ContextKey]interface{}) *HttpHandler {
	handler := &HttpHandler{
		Schema:           schema,
		Relay:            &relay.Handler{Schema: schema},
		ContextProviders: providers,
	}
	return handler
}

// Handles http request processing.
func (h *HttpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for key, value := range h.ContextProviders {
		r = r.WithContext(context.WithValue(r.Context(), key, value))
	}
	h.Relay.ServeHTTP(w, r)
}

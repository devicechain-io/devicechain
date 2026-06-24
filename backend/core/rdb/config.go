// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"encoding/json"

	"github.com/devicechain-io/dc-microservice/config"
)

type PostgresConfig struct {
	Hostname       string `json:"hostname"`
	MaxConnections int32  `json:"maxConnections"`
	Password       string `json:"password"`
	Port           int32  `json:"port"`
	Username       string `json:"username"`
}

// Use json marshaling to convert between generic config and strongly-typed.
func convertToPostgresConfig(rdb config.DatastoreConfiguration) (*PostgresConfig, error) {
	bytes, err := json.Marshal(rdb.Configuration)
	if err != nil {
		return nil, err
	}
	pgconf := &PostgresConfig{}
	err = json.Unmarshal(bytes, pgconf)
	if err != nil {
		return nil, err
	}
	return pgconf, nil
}

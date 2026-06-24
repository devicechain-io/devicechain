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

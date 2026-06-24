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
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// Manages lifecycle of relational database interactions.
type RdbManager struct {
	Microservice       *core.Microservice
	Database           *gorm.DB
	Migrations         []*gormigrate.Migration
	RedisCaches        map[string]*core.RedisCache
	InstanceConfig     config.DatastoreConfiguration
	MicroserviceConfig config.MicroserviceDatastoreConfiguration

	lifecycle core.LifecycleManager
}

// Create a new rdb manager.
func NewRdbManager(ms *core.Microservice, callbacks core.LifecycleCallbacks,
	migrations []*gormigrate.Migration, icfg config.DatastoreConfiguration,
	cfg config.MicroserviceDatastoreConfiguration) *RdbManager {
	rdb := &RdbManager{
		Microservice:       ms,
		Migrations:         migrations,
		RedisCaches:        make(map[string]*core.RedisCache),
		InstanceConfig:     icfg,
		MicroserviceConfig: cfg,
	}
	// Create lifecycle manager.
	rdbname := fmt.Sprintf("%s-%s", ms.FunctionalArea, "rdb")
	rdb.lifecycle = core.NewLifecycleManager(rdbname, rdb, callbacks)
	return rdb
}

// Query for a list of models based on filter and pagination criteria.
func (rdb *RdbManager) ListOf(mdl interface{}, filters func(db *gorm.DB) *gorm.DB, pag Pagination) (*gorm.DB, SearchResultsPagination) {
	// Sanity check page number.
	if pag.PageNumber < 1 {
		pag.PageNumber = 1
	}

	// Execute count query.
	count := int64(0)
	result := rdb.Database.Model(mdl)
	if filters != nil {
		result = filters(result)
	}
	_ = result.Count(&count)
	total := int32(count)

	// Execute data query.
	result = rdb.Database.Model(mdl)
	if filters != nil {
		result = filters(result)
	}
	result.Scopes(Paginate(pag))

	// Short-circuit for returning all.
	if pag.PageSize < 1 {
		return result, SearchResultsPagination{
			PageStart:    1,
			PageEnd:      total,
			TotalRecords: total,
		}
	}

	last := pag.PageNumber * pag.PageSize
	if total < last {
		last = total
	}

	srpag := SearchResultsPagination{
		PageStart:    (pag.PageNumber-1)*pag.PageSize + 1,
		PageEnd:      last,
		TotalRecords: total,
	}
	return result, srpag
}

// Create a new redis cache with the given settings.
func (rdb *RdbManager) NewRedisCache(name string, size int, ttl time.Duration) *core.RedisCache {
	created := core.NewRedisCache(*rdb.Microservice.Redis, name, size, ttl)
	rdb.RedisCaches[name] = created
	return created
}

// Get a redis cache by name.
func (rdb *RdbManager) GetRedisCache(name string) *core.RedisCache {
	return rdb.RedisCaches[name]
}

// Initialize component.
func (rdb *RdbManager) Initialize(ctx context.Context) error {
	return rdb.lifecycle.Initialize(ctx)
}

// Lifecycle callback that runs initialization logic.
func (rdb *RdbManager) ExecuteInitialize(context.Context) error {
	// Make sure database exists before interacting with it.
	dbtype := rdb.InstanceConfig.Type
	if strings.HasPrefix(dbtype, "postgres") {
		err := rdb.initializePostgres()
		if err != nil {
			return err
		}
	} else if strings.HasPrefix(dbtype, "timescaledb") {
		err := rdb.initializePostgres()
		if err != nil {
			return err
		}
	} else {
		return fmt.Errorf("relational database %s not currently supported", dbtype)
	}

	// Run migrations in a database independent manner.
	mtable := strings.ReplaceAll(fmt.Sprintf("%s_%s", rdb.Microservice.FunctionalArea, "migrations"), "-", "_")
	options := &gormigrate.Options{
		TableName:                 mtable,
		IDColumnName:              "id",
		IDColumnSize:              255,
		UseTransaction:            false,
		ValidateUnknownMigrations: false,
	}
	m := gormigrate.New(rdb.Database, options, rdb.Migrations)
	if err := m.Migrate(); err != nil {
		return err
	}

	return nil
}

// Start component.
func (rdb *RdbManager) Start(ctx context.Context) error {
	return rdb.lifecycle.Start(ctx)
}

// Lifecycle callback that runs startup logic.
func (rdb *RdbManager) ExecuteStart(context.Context) error {
	return nil
}

// Stop component.
func (rdb *RdbManager) Stop(ctx context.Context) error {
	return rdb.lifecycle.Stop(ctx)
}

// Lifecycle callback that runs shutdown logic.
func (rdb *RdbManager) ExecuteStop(context.Context) error {
	return nil
}

// Terminate component.
func (rdb *RdbManager) Terminate(ctx context.Context) error {
	return rdb.lifecycle.Terminate(ctx)
}

// Lifecycle callback that runs termination logic.
func (rdb *RdbManager) ExecuteTerminate(context.Context) error {
	sqldb, err := rdb.Database.DB()
	if err != nil {
		return err
	}
	sqldb.Close()
	return nil
}

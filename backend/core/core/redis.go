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

package core

import (
	"context"
	"fmt"

	"github.com/bsm/redislock"
	redis "github.com/go-redis/redis/v8"
	"github.com/rs/zerolog/log"
)

// Manages lifecycle of Redis interactions.
type RedisManager struct {
	Microservice *Microservice
	Client       *redis.Client
	RedisLock    *redislock.Client

	lifecycle LifecycleManager
}

// Create a new Redis manager.
func NewRedisManager(ms *Microservice, callbacks LifecycleCallbacks) *RedisManager {
	redis := &RedisManager{
		Microservice: ms,
	}

	// Create lifecycle manager.
	name := fmt.Sprintf("%s-%s", ms.FunctionalArea, "redis")
	redis.lifecycle = NewLifecycleManager(name, redis, callbacks)
	return redis
}

// Initialize component.
func (rmgr *RedisManager) Initialize(ctx context.Context) error {
	return rmgr.lifecycle.Initialize(ctx)
}

// Lifecycle callback that runs initialization logic.
func (rmgr *RedisManager) ExecuteInitialize(ctx context.Context) error {
	rconfig := rmgr.Microservice.InstanceConfiguration.Infrastructure.Redis
	url := fmt.Sprintf("%s:%d", rconfig.Hostname, rconfig.Port)

	rmgr.Client = redis.NewClient(&redis.Options{
		Addr:     url,
		Password: "",
		DB:       0,
		OnConnect: func(ctx context.Context, cn *redis.Conn) error {
			log.Info().Msg(fmt.Sprintf("Successfully connected to Redis at %s", url))
			return nil
		},
	})
	if status := rmgr.Client.Ping(ctx); status.Err() != nil {
		return status.Err()
	}
	log.Info().Msg(fmt.Sprintf("Verified successful Redis ping against %s", url))

	// Set up redis lock implementation using client.
	rmgr.RedisLock = redislock.New(rmgr.Client)

	return nil
}

// Start component.
func (rmgr *RedisManager) Start(ctx context.Context) error {
	return rmgr.lifecycle.Start(ctx)
}

// Lifecycle callback that runs startup logic.
func (rmgr *RedisManager) ExecuteStart(context.Context) error {
	return nil
}

// Stop component.
func (rmgr *RedisManager) Stop(ctx context.Context) error {
	return rmgr.lifecycle.Stop(ctx)
}

// Lifecycle callback that runs shutdown logic.
func (rmgr *RedisManager) ExecuteStop(context.Context) error {
	return nil
}

// Terminate component.
func (rmgr *RedisManager) Terminate(ctx context.Context) error {
	return rmgr.lifecycle.Terminate(ctx)
}

// Lifecycle callback that runs termination logic.
func (rmgr *RedisManager) ExecuteTerminate(context.Context) error {
	err := rmgr.Client.Close()
	if err != nil {
		return err
	}
	log.Info().Msg("Redis client closed successfully.")
	return nil
}

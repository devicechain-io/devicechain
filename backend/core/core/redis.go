// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

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
	// Retry the initial ping so a few seconds of Redis lag on a cluster restart
	// degrades into a retry rather than a crash-loop (A6).
	if err := RetryInfraConnect(ctx, "redis", func(ctx context.Context) error {
		return rmgr.Client.Ping(ctx).Err()
	}); err != nil {
		return err
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

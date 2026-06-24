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

package main

import (
	"context"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/rs/zerolog/log"
)

// Create and run a microservice
func main() {
	callbacks := core.LifecycleCallbacks{
		Initializer: core.LifecycleCallback{
			Preprocess: func(ctx context.Context) error {
				log.Info().Msg("Microservice called pre-initialize callback.")
				return nil
			},
			Postprocess: func(ctx context.Context) error {
				log.Info().Msg("Microservice called post-initialize callback.")
				return nil
			},
		},
		Starter: core.LifecycleCallback{
			Preprocess: func(ctx context.Context) error {
				log.Info().Msg("Microservice called pre-start callback.")
				return nil
			},
			Postprocess: func(ctx context.Context) error {
				log.Info().Msg("Microservice called post-start callback.")
				return nil
			},
		},
		Stopper: core.LifecycleCallback{
			Preprocess: func(ctx context.Context) error {
				log.Info().Msg("Microservice called pre-stop callback.")
				return nil
			},
			Postprocess: func(ctx context.Context) error {
				log.Info().Msg("Microservice called post-stop callback.")
				return nil
			},
		},
		Terminator: core.LifecycleCallback{
			Preprocess: func(ctx context.Context) error {
				log.Info().Msg("Microservice called pre-terminate callback.")
				return nil
			},
			Postprocess: func(ctx context.Context) error {
				log.Info().Msg("Microservice called post-terminate callback.")
				return nil
			},
		},
	}
	ms := core.NewMicroservice(callbacks)
	ms.Run()
}

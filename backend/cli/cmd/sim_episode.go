// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"github.com/devicechain-io/dcctl/sim"
	"github.com/spf13/pflag"
)

func registerEpisodeFlag(fs *pflag.FlagSet) {
	fs.String("episode", "", "towerpulse episode (classic or night-shift; default classic)")
}
func resolveEpisode(fs *pflag.FlagSet, manifest string) (string, error) {
	episode, err := fs.GetString("episode")
	if err != nil {
		return "", err
	}
	if err := sim.ValidateEpisode(manifest, episode, fs.Changed("episode")); err != nil {
		return "", err
	}
	return episode, nil
}

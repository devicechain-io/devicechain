// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The event wire format a service writes is the one its instance document names, applied
// by the load a service actually runs at startup: this is the WIRING, so it is tested
// through LoadInstanceConfigurationFrom and the process setting every producer reads, not
// through the config package alone.
func TestLoadInstanceConfigurationAppliesTheEventWireFormat(t *testing.T) {
	t.Cleanup(func() { require.NoError(t, eventtime.SetProducerWireFormat(eventtime.DefaultWireFormat)) })

	withFormat := func(f string) string {
		return `{"infrastructure":{"nats":{"hostname":"h","port":4222},
		         "userManagement":{"hostname":"u","port":8080},
		         "messaging":{"wireFormat":"` + f + `"}}}`
	}

	ms := &Microservice{}
	require.NoError(t, ms.LoadInstanceConfigurationFrom(instanceDoc(t, withFormat("compact"))))
	assert.Equal(t, eventtime.WireCompact, eventtime.ProducerWireFormat())
	assert.Equal(t, "compact", ms.InstanceConfiguration.Infrastructure.Messaging.WireFormat)

	ms = &Microservice{}
	require.NoError(t, ms.LoadInstanceConfigurationFrom(instanceDoc(t, withFormat("legacy"))))
	assert.Equal(t, eventtime.WireLegacy, eventtime.ProducerWireFormat())

	// Omitted: the default, which is compact. Set legacy first, so this cannot pass by
	// leaving the setting alone.
	require.NoError(t, eventtime.SetProducerWireFormat(eventtime.WireLegacy))
	ms = &Microservice{}
	require.NoError(t, ms.LoadInstanceConfigurationFrom(instanceDoc(t, minimalInstanceDoc)))
	assert.Equal(t, eventtime.WireCompact, eventtime.ProducerWireFormat())
	assert.Equal(t, "compact", ms.InstanceConfiguration.Infrastructure.Messaging.WireFormat)
}

// A value that names no format refuses the load and NAMES the key; the process setting is
// left as it was rather than defaulted.
func TestLoadInstanceConfigurationRefusesAnUnknownEventWireFormat(t *testing.T) {
	t.Cleanup(func() { require.NoError(t, eventtime.SetProducerWireFormat(eventtime.DefaultWireFormat)) })
	// The non-default format, so a load that wrongly fell back to the default would show.
	require.NoError(t, eventtime.SetProducerWireFormat(eventtime.WireLegacy))

	for _, value := range []string{"Compact", "binary", " legacy"} {
		ms := &Microservice{}
		err := ms.LoadInstanceConfigurationFrom(instanceDoc(t, `{"infrastructure":{"nats":{"hostname":"h","port":4222},
		         "userManagement":{"hostname":"u","port":8080},
		         "messaging":{"wireFormat":"`+value+`"}}}`))
		require.Error(t, err, "%q", value)
		assert.Contains(t, err.Error(), "wireFormat", "%q", value)
		assert.Equal(t, eventtime.WireLegacy, eventtime.ProducerWireFormat(), "%q changed the setting", value)
	}

	ms := &Microservice{}
	err := ms.LoadInstanceConfigurationFrom(instanceDoc(t, `{"infrastructure":{"nats":{"hostname":"h","port":4222},
	         "userManagement":{"hostname":"u","port":8080},
	         "messaging":{"wireFromat":"compact"}}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wireFromat")
}

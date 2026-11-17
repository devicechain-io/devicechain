// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package product

import (
	"encoding/json"
	"reflect"
)

func connectorRecipientChanged(d Connector, live *LiveConnector) bool {
	if d.Type != live.Type {
		return true
	}
	strip := func(raw []byte) any {
		var c map[string]any
		if json.Unmarshal(raw, &c) != nil {
			return string(raw)
		}
		keys := map[string][]string{
			"mqtt":             {"topic", "qos", "clientId", "username"},
			"kafka":            {"topic", "clientId"},
			"aws_sns":          {"accessKeyId", "topicArn"},
			"aws_sqs":          {"accessKeyId", "url"},
			"lorawan_downlink": {"integration", "applicationId"},
		}
		for _, k := range keys[d.Type] {
			delete(c, k)
		}
		if d.Type == "kafka" {
			if sasl, ok := c["sasl"].(map[string]any); ok {
				delete(sasl, "username")
			}
		}
		return c
	}
	return !reflect.DeepEqual(strip(d.Config), strip(live.Config))
}

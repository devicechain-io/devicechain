// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

// SparkplugExternalId is the ADR-049 external id for the device a Sparkplug message
// identifies: "{group}/{node}" for a node-level message, or "{group}/{node}/{device}"
// for a device-level one. This raw, slash-joined form is the customer-owned foreign
// identity — it is stored ONLY in a device's externalId (which carries no grammar),
// never as a token. It is meaningful only for node/device messages, not STATE.
//
// The token DERIVATION (raw external id → grammar-safe DeviceChain token) is
// protocol-neutral and lives in the shared adapter package (adapter.DeriveDeviceToken);
// only this Sparkplug-topic → external-id mapping is protocol-specific and stays here.
func SparkplugExternalId(t Topic) string {
	if t.IsDevice() {
		return t.GroupID + "/" + t.EdgeNodeID + "/" + t.DeviceID
	}
	return t.GroupID + "/" + t.EdgeNodeID
}

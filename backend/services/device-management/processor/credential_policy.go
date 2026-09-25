// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"time"

	"github.com/devicechain-io/dc-microservice/credential"
)

// DeviceCredentialPolicy is the backoff on MQTT password connects, per MQTT username
// (credential.KindDeviceCredential): ten failures in a row are free, then the next
// attempt waits 1 second, doubling up to 30.
//
// It is LENIENT on purpose, and more so than the policy on people's passwords. A
// device retries on its own schedule, with no person to tell, so ten misfires in a row
// is a misconfigured device far more often than an attack; and a device held at a
// long cap stays disconnected, not merely inconvenienced. 30 seconds is still enough
// to turn a guesser's thousands of attempts a second into two a minute per username.
//
// 🔴 THE COST, STATED PLAINLY (the credential package doc has the mechanism): the
// backoff is keyed on the USERNAME, which is not secret — anyone who can read the
// device's credentials knows it. Whoever keeps presenting a wrong password for it can
// take each evaluation slot as it opens and keep that device from reconnecting for as
// long as they keep it up; each delay is capped, the lockout is not. A connected device
// is unaffected until it reconnects, or until its user JWT expires and the broker
// makes it. There is no lockout that outlives the attack.
//
// credential.Policy.validate checks it at construction: 2 x 30s is well inside the
// ten-minute record TTL, so no record expires during the delay it enforces.
var DeviceCredentialPolicy = credential.Policy{Free: 10, Base: time.Second, Cap: 30 * time.Second}

// DeviceCredentialPolicies is the one kind this service throttles, for
// credential.NewChecker.
var DeviceCredentialPolicies = map[credential.Kind]credential.Policy{
	credential.KindDeviceCredential: DeviceCredentialPolicy,
}

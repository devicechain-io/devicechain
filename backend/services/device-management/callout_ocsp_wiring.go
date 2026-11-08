// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/processor"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-microservice/x509trust/ocspclient"
	"github.com/rs/zerolog/log"
)

// newOcspClient builds the responder client over the instance's tenant-egress boundary.
//
// TWO guards, deliberately. A responder an anchor's OPERATOR names (the per-anchor override) is
// dialled through the instance guard, so a destination the operator allowed for the egress
// boundary (an in-cluster relay, a /32) is reachable by that anchor. A responder URL read from a
// CERTIFICATE (its Authority Information Access entry) is chosen by whoever runs the tenant's CA,
// and the operator's allow list is process-wide: through the same guard it would let any tenant's
// certificate reach an address the operator allowed for something else. So AIA fetches go through
// a guard with NO allow list, which still refuses every private, loopback, link-local and metadata
// range.
func newOcspClient(cfg config.OcspConfiguration, infra mscfg.InfrastructureConfiguration, metrics *processor.OcspMetrics) (*ocspclient.Client, error) {
	operator, err := egress.FromConfig(infra.Egress)
	if err != nil {
		return nil, err
	}
	strict := egress.NewGuard(nil)
	var override, aia http.RoundTripper = operator.Transport(), strict.Transport()
	return ocspclient.New(ocspclient.Config{
		FetchDeadline:     cfg.FetchDeadlineDuration(),
		MaxInFlight:       cfg.MaxInFlight,
		MaxAge:            cfg.MaxAgeDuration(),
		FailureTTL:        cfg.FailureTtlDuration(),
		CacheEntries:      cfg.CacheEntries,
		MaxHostsPerTenant: cfg.MaxResponderHostsPerTenant,
	}, ocspclient.NewHTTPClient(override), ocspclient.NewHTTPClient(aia), metrics.Hooks()), nil
}

// wireCalloutOcsp attaches the OCSP check to the auth callout. With the master switch off nothing
// is built that could dial: the responder runs ENFORCE_KNOWN anchors as OFF and refuses
// REQUIRE_CURRENT ones.
func wireCalloutOcsp(resp *processor.CalloutResponder, metrics *processor.OcspMetrics, cfg *config.DeviceManagementConfiguration,
	infra mscfg.InfrastructureConfiguration) error {
	oc := cfg.Callout.Ocsp
	if !oc.Enabled {
		resp.WithOcsp(false, nil, metrics)
		log.Info().Msg("OCSP is switched off (deviceManagement.callout.ocsp.enabled): no responder is ever asked; " +
			"anchors set to ENFORCE_KNOWN run as OFF and anchors set to REQUIRE_CURRENT refuse new certificate connects.")
		return nil
	}
	client, err := newOcspClient(oc, infra, metrics)
	if err != nil {
		return err
	}
	resp.WithOcsp(true, client, metrics)
	log.Info().Dur("fetchDeadline", oc.FetchDeadlineDuration()).Int("maxInFlight", oc.MaxInFlight).Dur("maxAge", oc.MaxAgeDuration()).
		Bool("allowRequireCurrent", oc.AllowRequireCurrent).Msg("Device auth-callout checks OCSP for the anchors that ask for it.")
	return nil
}

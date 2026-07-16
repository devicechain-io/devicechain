// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
)

// NATS configuration parameters
type NatsConfiguration struct {
	Hostname string
	Port     uint32
	// StreamReplicas is the JetStream replica count for created streams
	// (1 for single-node dev; raise to 3 for the HA topology in ADR-018).
	StreamReplicas uint32
	// StreamMaxBytes / StreamMaxMsgs bound the on-disk size and message count of
	// each per-suffix JetStream stream (ADR-023): a PER-STREAM platform ceiling so a
	// producer flood — or a wedged consumer that stops draining — cannot grow one
	// stream without limit. Paired with DiscardOld retention, hitting a ceiling
	// evicts the OLDEST messages (the same backpressure as the 7-day age window, by
	// size instead of time), so size these for the retention a busy cluster needs.
	// Note this is a per-stream bound, not an aggregate-disk guarantee: the true
	// disk ceiling is (stream count × StreamMaxBytes), which must fit the broker's
	// JetStream store — the default keeps the ~8 streams within a modest PV. (An
	// account-level max_file_store at the broker is the belt to this suspenders;
	// tracked separately.) StreamMaxMsgSize rejects an oversized single message at
	// publish. All three are fail-safe (ADR-023 never-unlimited): a zero, negative,
	// or unset value is coerced to the platform default in ApplyDefaults rather
	// than left at 0, which JetStream would treat as UNLIMITED. Raise them for a
	// high-throughput deployment; there is deliberately no unlimited setting.
	StreamMaxBytes   int64
	StreamMaxMsgs    int64
	StreamMaxMsgSize int32
	// Tls, when enabled, makes clients dial the broker over TLS and verify the
	// server certificate against Ca (ADR-025). The broker terminates TLS on both
	// the 4222 client listener and the 1883 MQTT gateway with a cert this CA
	// signs, so this single flag governs every client connection. Server-auth
	// only in v1 — no client certificate is presented (device authentication is
	// the separate auth-callout half of ADR-025).
	Tls NatsTlsConfiguration
	// Auth carries the broker-authentication material once auth callout is enabled
	// (ADR-025): the shared service credential every internal service presents, and
	// (device-management only) the callout issuer seed.
	Auth NatsAuthConfiguration
}

// NatsAuthConfiguration is the broker-authentication material threaded into the
// instance config once auth callout is enabled (ADR-025).
type NatsAuthConfiguration struct {
	// User / Password are the shared static service credential every internal
	// service presents to the broker (the `dc_service` login in the callout's
	// auth_users, so service connections bypass the device callout). Empty means
	// the broker requires no client auth (pre-cutover), and clients connect
	// anonymously.
	User     string
	Password string
	// CalloutIssuerSeed is the account nkey seed the device-management auth-callout
	// responder signs device user JWTs with. Only device-management consumes it; it
	// is carried in every service's instance config for provisioning simplicity
	// (all services already share the instance config's secrets — a trusted-
	// boundary tradeoff to revisit if per-service isolation is needed). Empty
	// elsewhere and when the callout is disabled.
	CalloutIssuerSeed string
}

// NatsTlsConfiguration controls client-side TLS to the NATS broker (ADR-025).
type NatsTlsConfiguration struct {
	Enabled bool
	// Ca is the PEM-encoded CA certificate(s) that signed the NATS server cert.
	// The broker lives in the shared infra namespace, so rather than mount its
	// cert Secret cross-namespace, the bring-up threads the CA into this instance
	// config (which every service already loads) from the OpenTofu nats_ca output.
	Ca string
}

// TLSConfig builds the client tls.Config for connecting to NATS with the given
// serverName (matched against the certificate SANs), or (nil, nil) when TLS is
// disabled. Callers pass nil straight through to leave the connection plaintext.
func (c NatsConfiguration) TLSConfig(serverName string) (*tls.Config, error) {
	if !c.Tls.Enabled {
		return nil, nil
	}
	if c.Tls.Ca == "" {
		return nil, fmt.Errorf("infrastructure.nats.tls is enabled but tls.ca is empty")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(c.Tls.Ca)) {
		return nil, fmt.Errorf("infrastructure.nats.tls.ca contained no valid PEM certificates")
	}
	return &tls.Config{
		RootCAs:    pool,
		ServerName: serverName,
		MinVersion: tls.VersionTLS12,
	}, nil
}

// Prometheus metrics configuration. Metrics are served on the GraphQL HTTP port
// (8080) at /metrics via the shared mux; there is no separate metrics listener,
// so the former HttpPort field was dead config and has been removed (E14).
// Enabled is informational — scrape discovery is gated by the Helm chart's
// metrics.enabled value (the ServiceMonitor), not by the running service.
type MetricsConfiguration struct {
	Enabled bool
}

// User-management connectivity configuration. The user-management service
// issues the platform's RS256 JWT signing key (ADR-008); every other service
// fetches the public key from this host at startup to validate access tokens.
type UserManagementConfiguration struct {
	Hostname string
	Port     uint32
}

// DeviceManagementConfiguration locates the device-management GraphQL endpoint for
// synchronous cross-service calls (ADR-044 amendment) — e.g. command-delivery
// verifying a target device exists before enqueue (W1.1b). Only that caller
// consumes it, and only when the service secret is also set, so it is neither
// required by Validate nor filled by ApplyDefaults; command-delivery guards on it
// at startup (warn-and-skip if unset). The Helm chart supplies the in-cluster
// coordinate for a normal deploy.
type DeviceManagementConfiguration struct {
	Hostname string
	Port     uint32
}

// EventProcessingConfiguration locates the event-processing GraphQL endpoint for
// synchronous cross-service calls (ADR-044 amendment) — device-management compiling a
// profile's draft detection rules at publish (ADR-051 slice 4b), the reverse direction
// of the command-delivery→device-management device check. Only that caller consumes it,
// and only when the service secret is also set, so it is neither required by Validate
// nor filled by ApplyDefaults; device-management guards on it at startup (warn-and-skip
// if unset). The Helm chart supplies the in-cluster coordinate for a normal deploy.
type EventProcessingConfiguration struct {
	Hostname string
	Port     uint32
}

// CommandDeliveryConfiguration locates the command-delivery GraphQL endpoint for
// synchronous cross-service calls (ADR-044 amendment) — event-processing's REACT
// dispatcher enqueuing a command when a detection rule's sendCommand action fires
// (ADR-051 slice 5b). Only that caller consumes it, and only when the service secret
// is also set, so it is neither required by Validate nor filled by ApplyDefaults;
// event-processing guards on it at startup (warn-and-skip if unset, so REACT
// send-command is inert rather than a startup failure). The Helm chart supplies the
// in-cluster coordinate for a normal deploy.
type CommandDeliveryConfiguration struct {
	Hostname string
	Port     uint32
}

// AiInferenceConfiguration locates the ai-inference GraphQL endpoint for the
// synchronous cross-service call event-processing makes to draft a detection rule
// from natural language (ADR-056 slice 1): event-processing carries the human's
// NL prompt to the active provider over a service token (least-privilege ai:infer)
// and runs the returned candidate through its own rules.Compile firewall. Only that
// caller consumes it, and only when the service secret is also set, so it is neither
// required by Validate nor filled by ApplyDefaults; event-processing guards on it at
// startup (warn-and-skip if unset, so NL drafting is cleanly UNAVAILABLE rather than a
// startup failure). The Helm chart supplies the in-cluster coordinate for a normal
// deploy. ai-inference is an OPT-IN area, so this is commonly unset.
type AiInferenceConfiguration struct {
	Hostname string
	Port     uint32
}

// ServiceAuthConfiguration carries the shared secret backing the synchronous
// cross-service call primitive (ADR-044 amendment). A caller presents Secret to
// user-management's mint endpoint to obtain a short-lived service token; the mint
// endpoint compares it (constant-time) against its copy of the same value. It is
// threaded into every service's instance config for provisioning simplicity (the
// same trusted-boundary tradeoff NatsAuth already makes — all services share the
// instance config's secrets). Empty disables service-token minting: user-management
// refuses to mint and svcclient refuses to call, both fail-closed.
type ServiceAuthConfiguration struct {
	Secret string
}

// Default secret-store backend and KEK provider (ADR-059). These mirror the
// canonical identifiers in the secrets package (secrets.BackendPostgres /
// secrets.InstanceKEKProvider); they are duplicated here as literals rather than
// imported because rdb→config, so a config→secrets→rdb import would cycle. The
// secrets package's own Config.Validate is the authoritative check of the selected
// identifiers at wiring time.
const (
	DefaultSecretsBackend     = "postgres"
	DefaultSecretsKEKProvider = "instance"
)

// SecretsConfiguration selects the secret-storage backend and, for the Postgres
// backend, the KEK provider, plus (for the default instance KEK provider) the root
// key that wraps every per-secret DEK (ADR-059). RootKey is the one new piece of
// instance secret material: a base64-encoded 256-bit key delivered via the instance
// K8s Secret, the same channel as the NATS and service-auth credentials. It is not
// required for a service that does not use the secret store; a service that DOES use
// it forms its KEK via DecodedRootKey, which fails closed on an absent or malformed
// key so it cannot silently run without encryption.
type SecretsConfiguration struct {
	// Backend selects where a value lives. Default "postgres" (envelope-encrypted
	// in the service DB). External secret-manager backends are additive (ADR-059).
	Backend string
	// KEKProvider selects DEK wrapping for the Postgres backend. Default "instance"
	// (the RootKey below). Cloud-KMS providers are additive.
	KEKProvider string
	// RootKey is the base64-encoded 256-bit instance root key for the instance KEK
	// provider. Empty when the deployment does not use the secret store (or uses an
	// external backend/KMS that owns its own keys).
	RootKey string
}

// DecodedRootKey decodes and validates the instance root key. It fails closed on an
// absent, malformed, or wrong-length key so a service that constructs the secret
// store cannot start without a usable KEK (ADR-059: "a service that cannot form its
// KEK must not start"). It is called at store construction, not by Validate, so a
// service that does not use secrets is not forced to configure a key.
func (c SecretsConfiguration) DecodedRootKey() ([]byte, error) {
	if c.RootKey == "" {
		return nil, fmt.Errorf("infrastructure.secrets.rootKey is not configured; a service using the secret store cannot form its instance KEK")
	}
	raw, err := base64.StdEncoding.DecodeString(c.RootKey)
	if err != nil {
		return nil, fmt.Errorf("infrastructure.secrets.rootKey is not valid base64: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("infrastructure.secrets.rootKey decodes to %d bytes, want 32 (256-bit)", len(raw))
	}
	return raw, nil
}

// DefaultBlobBackend is the object-store backend selected when none is configured:
// the local filesystem/PVC (ADR-058). Declared here (not imported from core/blob)
// for the same reason as the secrets defaults above — the blob package's own
// Config.Validate is the authoritative check, run at store construction in the
// service that uses it (fail-closed at startup on an unknown/unbuilt backend).
const DefaultBlobBackend = "filesystem"

// BlobConfiguration selects the object/asset-store backend and carries the
// filesystem root for the default backend (ADR-058). It is instance-infra config,
// like Secrets: only a service that constructs a blob store needs it (today,
// user-management for branding logos). Cloud-backend settings (bucket/region/
// endpoint) are additive fields introduced with those backends; no cloud
// credential is ever a plaintext value here (ADR-058 §5 — those resolve from the
// instance secret material).
type BlobConfiguration struct {
	// Backend selects where objects live. Default "filesystem". S3/GCS are additive.
	Backend string
	// Directory is the filesystem-backend root (a mounted volume/PVC). Required by a
	// service that constructs a filesystem-backed store; the store's construction
	// fails closed on an empty directory.
	Directory string

	// S3-backend settings (backend "s3"), non-secret only. Bucket is required;
	// Region is the AWS region (or default when only Endpoint is set); Endpoint
	// targets an S3-compatible service (MinIO); UsePathStyle forces path-style
	// addressing (MinIO). Credentials come from the standard AWS chain (env from the
	// instance K8s Secret, IRSA, or an instance profile), never from here (ADR-058 §5).
	Bucket       string
	Region       string
	Endpoint     string
	UsePathStyle bool
}

// Infrastructure configuration section
type InfrastructureConfiguration struct {
	Nats             NatsConfiguration
	Metrics          MetricsConfiguration
	UserManagement   UserManagementConfiguration
	DeviceManagement DeviceManagementConfiguration
	EventProcessing  EventProcessingConfiguration
	CommandDelivery  CommandDeliveryConfiguration
	AiInference      AiInferenceConfiguration
	ServiceAuth      ServiceAuthConfiguration
	Secrets          SecretsConfiguration
	Blob             BlobConfiguration
}

// Generic datastore configuration
type DatastoreConfiguration struct {
	Type          string
	Configuration map[string]interface{}
}

// Configuration of persistence stores
type PersistenceConfiguration struct {
	Rdb  DatastoreConfiguration
	Tsdb DatastoreConfiguration
}

// Instance-level configuration settings
type InstanceConfiguration struct {
	Infrastructure InfrastructureConfiguration
	Persistence    PersistenceConfiguration
}

// Platform-default JetStream stream bounds (ADR-023). Sized so the platform's
// handful of per-suffix streams (~8) stay within a modest JetStream PV (8 × 1 GiB
// = 8 GiB) while leaving generous room for a normal 7-day retention window; a
// high-throughput deployment raises them via config. The message-size default
// mirrors the broker's default max_payload (1 MiB) so the stream ceiling reflects
// the limit actually enforced at publish rather than an inert larger value. See
// NatsConfiguration for the fail-safe rules.
const (
	DefaultStreamMaxBytes   int64 = 1 << 30   // 1 GiB per stream
	DefaultStreamMaxMsgs    int64 = 5_000_000 // 5M messages per stream
	DefaultStreamMaxMsgSize int32 = 1 << 20   // 1 MiB per message (matches default max_payload)
)

// ApplyDefaults fills unset infrastructure fields with their defaults so an
// instance document that omits them is still well-formed (ADR-022 decision 1 /
// review E3). It is applied after decoding and before Validate.
func (c *InstanceConfiguration) ApplyDefaults() {
	nats := &c.Infrastructure.Nats
	if nats.StreamReplicas == 0 {
		nats.StreamReplicas = 1
	}
	// Coerce any non-positive stream bound to the platform default: a 0 left in the
	// StreamConfig means UNLIMITED to JetStream, which would defeat the ceiling
	// (ADR-023 never-unlimited). An operator raises a bound by setting it explicitly.
	if nats.StreamMaxBytes <= 0 {
		nats.StreamMaxBytes = DefaultStreamMaxBytes
	}
	if nats.StreamMaxMsgs <= 0 {
		nats.StreamMaxMsgs = DefaultStreamMaxMsgs
	}
	if nats.StreamMaxMsgSize <= 0 {
		nats.StreamMaxMsgSize = DefaultStreamMaxMsgSize
	}
	// Default the secret-store selection so an instance document that omits it means
	// "the zero-infra default" (envelope-in-Postgres, instance KEK), not an invalid
	// empty selection. RootKey is deliberately NOT defaulted — key material is
	// supplied via the K8s Secret, never synthesized here.
	secrets := &c.Infrastructure.Secrets
	if secrets.Backend == "" {
		secrets.Backend = DefaultSecretsBackend
	}
	if secrets.KEKProvider == "" {
		secrets.KEKProvider = DefaultSecretsKEKProvider
	}
	// Default the object-store backend so an instance document that omits it means
	// "the zero-cloud default" (filesystem/PVC), not an invalid empty selection. The
	// Directory is deliberately NOT defaulted — it is a deployment-specific mount
	// path supplied via Helm, and the store construction fails closed if it is
	// missing for a service that uses the store.
	if c.Infrastructure.Blob.Backend == "" {
		c.Infrastructure.Blob.Backend = DefaultBlobBackend
	}
}

// Validate fails closed on an instance configuration missing the infrastructure
// a service cannot run without (ADR-022 decision 1 / review E3): the NATS
// backbone and the user-management endpoint every service validates tokens
// against. A misrendered config Secret then surfaces at startup rather than as a
// confusing downstream connection failure.
func (c *InstanceConfiguration) Validate() error {
	if c.Infrastructure.Nats.Hostname == "" || c.Infrastructure.Nats.Port == 0 {
		return fmt.Errorf("infrastructure.nats hostname and port are required")
	}
	if c.Infrastructure.UserManagement.Hostname == "" || c.Infrastructure.UserManagement.Port == 0 {
		return fmt.Errorf("infrastructure.userManagement hostname and port are required")
	}
	// A configured secret-store root key must be well-formed so a misrendered key
	// surfaces at startup rather than as a decrypt failure later. Absence is allowed
	// here — only a service that actually constructs the store requires a key, and
	// that path fails closed via DecodedRootKey.
	if c.Infrastructure.Secrets.RootKey != "" {
		if _, err := c.Infrastructure.Secrets.DecodedRootKey(); err != nil {
			return err
		}
	}
	return nil
}

// Creates the default instance configuration
func NewDefaultInstanceConfiguration() *InstanceConfiguration {
	return &InstanceConfiguration{
		Infrastructure: InfrastructureConfiguration{
			Nats: NatsConfiguration{
				Hostname:         "dc-nats.dc-system",
				Port:             4222,
				StreamReplicas:   1,
				StreamMaxBytes:   DefaultStreamMaxBytes,
				StreamMaxMsgs:    DefaultStreamMaxMsgs,
				StreamMaxMsgSize: DefaultStreamMaxMsgSize,
			},
			Metrics: MetricsConfiguration{
				Enabled: true,
			},
			UserManagement: UserManagementConfiguration{
				Hostname: "dc-user-management.dc-system",
				Port:     8080,
			},
			DeviceManagement: DeviceManagementConfiguration{
				Hostname: "dc-device-management.dc-system",
				Port:     8080,
			},
			EventProcessing: EventProcessingConfiguration{
				Hostname: "dc-event-processing.dc-system",
				Port:     8080,
			},
			Secrets: SecretsConfiguration{
				Backend:     DefaultSecretsBackend,
				KEKProvider: DefaultSecretsKEKProvider,
			},
		},
		Persistence: PersistenceConfiguration{
			Rdb: DatastoreConfiguration{
				Type: "postgres95",
				Configuration: map[string]interface{}{
					"hostname":       "dc-postgresql.dc-system",
					"port":           5432,
					"maxConnections": 5,
					"username":       "devicechain",
					"password":       "devicechain",
				},
			},
			Tsdb: DatastoreConfiguration{
				Type: "timescaledb",
				Configuration: map[string]interface{}{
					"hostname":       "dc-timescaledb-single.dc-system",
					"port":           5432,
					"maxConnections": 5,
					"username":       "postgres",
					"password":       "devicechain",
				},
			},
		},
	}
}

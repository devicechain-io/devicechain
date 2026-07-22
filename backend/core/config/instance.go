// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"

	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/streams"
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
	// disk ceiling is the SUM of every stream's ceiling (see StreamMaxBytesFor),
	// which must fit the broker's JetStream store. The belt to that suspenders is
	// the account-level max_file_store, which the deployment sets from the PV size
	// (jetstream_max_file_store in deploy/opentofu/modules/nats/main.tf); this sum
	// is what has to fit under it. StreamMaxMsgSize rejects an oversized single message at
	// publish. All three are fail-safe (ADR-023 never-unlimited): a zero, negative,
	// or unset value is coerced to the platform default in ApplyDefaults rather
	// than left at 0, which JetStream would treat as UNLIMITED. Raise them for a
	// high-throughput deployment; there is deliberately no unlimited setting.
	//
	// StreamMaxBytes applies to the HOT streams — those whose volume scales with
	// device count × message rate (inbound-events, resolved-events, …). The
	// control-plane streams, whose volume is driven by human/CRUD activity and is
	// orders of magnitude lower, take StreamMaxBytesCold instead; see
	// streams.All in core/streams for the classification, and StreamMaxBytesFor
	// for the lookup. Splitting them is what keeps the aggregate reservation small: the
	// ceiling is reserved UP FRONT at stream creation, so a uniform bound sizes
	// every control-plane stream for a load it will never see.
	StreamMaxBytes     int64
	StreamMaxBytesCold int64
	// MqttStoreMaxBytes bounds each of the MQTT gateway's OWN JetStream streams
	// ($MQTT_msgs, $MQTT_out). nats-server creates them with no size limit at all
	// and offers no option to bound them, yet they sit in the same account and
	// count against the same max_file_store as the platform's streams — so an
	// unbounded $MQTT_msgs can consume the headroom this budget leaves and produce
	// the very crashloop the ceilings prevent. Applied by
	// messaging.ReconcileMqttStores at startup. Fail-safe like the rest: a
	// non-positive value is coerced to the default rather than left meaning
	// UNLIMITED.
	MqttStoreMaxBytes int64
	// MqttQoS2StoreMaxBytes bounds the gateway's QoS 2 stores ($MQTT_out,
	// $MQTT_qos2in). Smaller than MqttStoreMaxBytes on purpose: QoS 2 is not the
	// recommended publish mode, so its buffers should not reserve disk the
	// recommended configuration never uses. $MQTT_qos2in is the one gateway stream
	// a device can fill deliberately — a QoS 2 publish is held until its PUBREL
	// arrives — and it discards NEW on full, so this ceiling turns that from a
	// disk-exhaustion vector into a refused publish.
	MqttQoS2StoreMaxBytes int64
	// KvCacheMaxBytes / KvStateMaxBytes bound each JetStream KV bucket
	// (messaging.KeyValueStore). KV buckets are backed by KV_-prefixed streams in
	// the same account and draw on the same max_file_store, but until now nothing
	// bounded or counted them — they shared whatever headroom was left over.
	//
	// The split keys on what a FULL bucket costs, not on how much it holds. nats.go
	// creates every KV bucket with DiscardNew, so a full one refuses writes rather
	// than evicting: a cache bucket degrades to a miss and the read falls through
	// to the database, while a state bucket fails the login, token exchange, or
	// lock acquisition that needed it. So the state tier takes the LARGER ceiling
	// even though the cache tier holds far more entries — see kv.All for the
	// per-bucket classification and kv.TierFor for the lookup.
	//
	// Fail-safe like the rest (ADR-023 never-unlimited): a non-positive value is
	// coerced to the platform default rather than left at 0, which JetStream would
	// read as UNLIMITED.
	KvCacheMaxBytes  int64
	KvStateMaxBytes  int64
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

// DeviceStateConfiguration locates the device-state GraphQL endpoint for synchronous
// cross-service reads (ADR-044 amendment) — the Sparkplug adapter enumerating a
// tenant's asserted-active devices on failover to reconcile presence (ADR-067 SP4b).
// Only that caller consumes it, and only when the service secret is also set, so it is
// neither required by Validate nor filled by ApplyDefaults; sparkplug-ingest guards on
// it at startup (fail-closed if unset, since reconciliation is a correctness guarantee,
// not a degrade). The Helm chart supplies the in-cluster coordinate for a normal deploy.
type DeviceStateConfiguration struct {
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
	DeviceState      DeviceStateConfiguration
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

// Platform-default JetStream stream bounds (ADR-023). JetStream reserves a
// stream's MaxBytes UP FRONT at creation, so the platform's true disk floor is
// the SUM of the per-stream ceilings — not what the streams actually hold. The
// platform creates one stream per suffix (15 today, a fixed set: streams are
// per-suffix and capture every tenant via the wildcard subject, so this does NOT
// grow with tenant count).
//
// A uniform 1 GiB across all 15 reserved 15 GiB, which forced a 32 GiB PV — and
// spent most of it on control-plane streams that will never hold more than a few
// MiB. Splitting hot from cold reserves (7 × 1 GiB) + (8 × 128 MiB) = 8 GiB,
// which fits a 12 GiB PV with the HOT streams keeping their full buffer. Halving
// the bound uniformly would have hit the same disk target only by halving the
// ingest buffer — the one place buffering earns its keep.
//
// The ceiling that has to hold is max_file_store, which is 10 GiB on that 12 GiB
// PV — the deployment floors 90% of the PV's MAGNITUDE and reattaches the unit
// (floor(12 * 0.9) = 10 → "10Gi"), so it is NOT 10.8 GiB. See the shared
// constants in instance_test.go, which pin the arithmetic against that real
// ceiling rather than a nominal 90%.
//
// The message-size default mirrors the broker's default max_payload (1 MiB) so
// the stream ceiling reflects the limit actually enforced at publish rather than
// an inert larger value. See NatsConfiguration for the fail-safe rules.
const (
	DefaultStreamMaxBytes     int64 = 1 << 30   // 1 GiB per hot stream
	DefaultStreamMaxBytesCold int64 = 128 << 20 // 128 MiB per control-plane stream
	DefaultStreamMaxMsgs      int64 = 5_000_000 // 5M messages per stream
	DefaultStreamMaxMsgSize   int32 = 1 << 20   // 1 MiB per message (matches default max_payload)
	// DefaultMqttStoreMaxBytes bounds each MQTT gateway stream. Sized as a
	// working buffer, not a retention window: $MQTT_msgs drains as the QoS-1
	// subscriber acks, so it holds in-flight messages rather than history. It is
	// deliberately small — its whole purpose is to cap an otherwise unlimited
	// store, and every byte reserved here is a byte the platform streams cannot
	// use.
	DefaultMqttStoreMaxBytes int64 = 256 << 20 // 256 MiB for the QoS>=1 message store
	// DefaultMqttQoS2StoreMaxBytes bounds each QoS 2 store. Deliberately small: QoS
	// 2 is not recommended here, so these are a safety cap on an otherwise
	// unlimited store rather than a working buffer sized for throughput.
	DefaultMqttQoS2StoreMaxBytes int64 = 64 << 20 // 64 MiB per QoS 2 store
	// DefaultKvCacheMaxBytes bounds each cache bucket. These are the KV buckets
	// that scale with fleet size, so they are the ones that would otherwise consume
	// the headroom — and they are also the ones where hitting the ceiling is
	// cheapest, since a refused write is a cache miss and the next read falls
	// through to the database. Sizing them below the state tier is therefore the
	// safe direction, not a compromise: the cost of being wrong here is latency.
	DefaultKvCacheMaxBytes int64 = 64 << 20 // 64 MiB per cache bucket
	// DefaultKvStateMaxBytes bounds each state bucket. Larger than a cache bucket
	// because a refused write here fails a login, an OAuth code exchange, or a lock
	// acquisition rather than degrading to a database read. What makes that bound
	// safe at all is that none of these scales with fleet size — they scale with
	// concurrent humans and concurrent reconcilers, and every entry carries a TTL,
	// so the working set is bounded by activity rather than by device count.
	DefaultKvStateMaxBytes int64 = 128 << 20 // 128 MiB per state bucket
)

// KvMaxBytesFor is the ceiling for the named KV bucket, keyed on its declared
// tier (kv.All). An unregistered bucket takes the state ceiling — see kv.TierFor
// for why the unknown case takes the larger one.
func (c *NatsConfiguration) KvMaxBytesFor(bucket string) int64 {
	if kv.TierFor(bucket) == kv.Cache {
		if c.KvCacheMaxBytes > 0 {
			return c.KvCacheMaxBytes
		}
		return DefaultKvCacheMaxBytes
	}
	if c.KvStateMaxBytes > 0 {
		return c.KvStateMaxBytes
	}
	return DefaultKvStateMaxBytes
}

// KvReservation is the disk every declared KV bucket reserves up front.
//
// A KV bucket is a stream, so its MaxBytes is reserved at creation exactly like a
// message stream's — which is what makes bounding them an accounting improvement
// and not merely a safety cap. Before this existed the buckets were unbounded, so
// they could not appear in the budget at all: the reservation understated the
// true floor and the difference was tracked as "headroom" that anything could
// eat. Counting them converts that hope into arithmetic.
//
// It derives from kv.All rather than a hand-maintained count, so declaring a new
// bucket raises the reservation automatically. That is the same failure this
// codebase already hit when the platform's own stream set was mirrored by hand
// (see core/streams), and it is not worth hitting twice.
func (c *NatsConfiguration) KvReservation() int64 {
	var total int64
	for _, b := range kv.All {
		total += c.KvMaxBytesFor(b.Name)
	}
	return total
}

// MqttGatewayStreamCount is how many MQTT gateway streams
// messaging.ReconcileMqttStores bounds: one message store plus two QoS 2 stores.
//
// It is a count here rather than the stream names themselves because core/config
// cannot import core/messaging — messaging depends on config, not the reverse.
// That is the same layering that made the platform's own stream set unknowable
// from here until core/streams existed, so it gets the same treatment:
// TestMqttGatewayStreamCountMatchesReconciler in the messaging package asserts
// this matches the set actually reconciled, and that the reservation below equals
// the sum of the ceilings actually applied.
const MqttGatewayStreamCount = 3

// MqttStoreReservation is the disk the MQTT gateway's bounded streams reserve up
// front: the message store plus the two QoS 2 stores. Once
// messaging.ReconcileMqttStores gives them a ceiling, JetStream reserves it like
// any other stream's — so this belongs in the same budget as the platform's own.
func (c *NatsConfiguration) MqttStoreReservation() int64 {
	return c.MqttStoreMaxBytes + 2*c.MqttQoS2StoreMaxBytes
}

func (c *NatsConfiguration) StreamMaxBytesFor(suffix string) int64 {
	tier := c.tierMaxBytesFor(suffix)
	// A stream may cap itself BELOW its tier — for one whose tier is right but whose
	// tier ceiling does not fit the budget. The tier says what drives the volume,
	// which is a different question from how much disk there is to give it.
	//
	// The smaller of the two wins, so the cap can only ever lower a ceiling. That
	// direction is the whole point: the tier ceilings are what an operator sizes a
	// deployment with (--compact runs them far below the defaults), and a cap that
	// could RAISE one would silently overrun a budget the volume was sized from.
	//
	// Because the reservation is summed through this function, a capped ceiling is
	// counted automatically; there is no second place to remember it. It is
	// deliberately not operator-tunable — an operator raises StreamMaxBytes /
	// StreamMaxBytesCold and the PV together, as those fields' own comments describe.
	if cap := streams.MaxBytesCapFor(suffix); cap > 0 && cap < tier {
		return cap
	}
	return tier
}

// tierMaxBytesFor is the ceiling a suffix's TIER alone would give it, before any
// per-stream cap.
func (c *NatsConfiguration) tierMaxBytesFor(suffix string) int64 {
	if streams.TierFor(suffix) == streams.Cold {
		if c.StreamMaxBytesCold > 0 {
			return c.StreamMaxBytesCold
		}
		return DefaultStreamMaxBytesCold
	}
	if c.StreamMaxBytes > 0 {
		return c.StreamMaxBytes
	}
	return DefaultStreamMaxBytes
}

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
	if nats.StreamMaxBytesCold <= 0 {
		nats.StreamMaxBytesCold = DefaultStreamMaxBytesCold
	}
	if nats.StreamMaxMsgs <= 0 {
		nats.StreamMaxMsgs = DefaultStreamMaxMsgs
	}
	if nats.StreamMaxMsgSize <= 0 {
		nats.StreamMaxMsgSize = DefaultStreamMaxMsgSize
	}
	if nats.MqttStoreMaxBytes <= 0 {
		nats.MqttStoreMaxBytes = DefaultMqttStoreMaxBytes
	}
	if nats.MqttQoS2StoreMaxBytes <= 0 {
		nats.MqttQoS2StoreMaxBytes = DefaultMqttQoS2StoreMaxBytes
	}
	if nats.KvCacheMaxBytes <= 0 {
		nats.KvCacheMaxBytes = DefaultKvCacheMaxBytes
	}
	if nats.KvStateMaxBytes <= 0 {
		nats.KvStateMaxBytes = DefaultKvStateMaxBytes
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
	if err := c.Infrastructure.Nats.validateTierOrdering(); err != nil {
		return err
	}
	return nil
}

// validateTierOrdering rejects a JetStream ceiling pair whose two tiers have been
// inverted.
//
// Both splits below exist to make the disk reservation affordable, and both do it
// the same way: the tier that grows — with device traffic, with fleet size — takes
// the SMALLER ceiling, and the tier that cannot grow takes the larger one. That
// ordering is not a stylistic preference, it is the entire mechanism. Invert it
// and the bound sits on the wrong side of the thing it is meant to bound: the
// control-plane streams become the largest on disk, or the fleet-scaling caches do
// — and in both cases the reservation the budget test checks is still satisfied,
// because the SUM barely moves. Nothing else would notice.
//
// The realistic way to get here is not a typo but a partial edit: lowering
// streamMaxBytes for a small-footprint deployment and leaving streamMaxBytesCold
// at its default silently makes every control-plane stream the biggest one in the
// instance. That is exactly what the compact preset does to the hot bound, which
// is why this guard lands before it.
//
// It rejects rather than clamping. A silent clamp is server-side inference of an
// operator's intent, which this codebase does not do (CLAUDE.md fail-closed): an
// operator who writes two numbers meant both of them, and the one to correct is
// theirs to choose.
//
// The MQTT pair (MqttQoS2StoreMaxBytes vs MqttStoreMaxBytes) is deliberately NOT
// guarded here. Those two are sized by which publish mode is recommended, not by
// which one scales, so inverting them wastes disk on a discouraged path without
// putting the smaller bound on the growing side. There is no invariant to break.
func (c *NatsConfiguration) validateTierOrdering() error {
	for _, p := range []struct {
		smaller, larger           string
		smallerValue, largerValue int64
		why                       string
	}{
		{
			smaller: "streamMaxBytesCold", smallerValue: c.StreamMaxBytesCold,
			larger: "streamMaxBytes", largerValue: c.StreamMaxBytes,
			why: "the cold bound applies to control-plane streams, whose volume cannot " +
				"scale with device count, so it must not exceed the hot bound",
		},
		{
			smaller: "kvCacheMaxBytes", smallerValue: c.KvCacheMaxBytes,
			larger: "kvStateMaxBytes", largerValue: c.KvStateMaxBytes,
			why: "the cache ceiling applies to the KV buckets that scale with fleet " +
				"size, so it must not exceed the state ceiling",
		},
	} {
		// A zero on either side means the value has not been defaulted yet; leave it
		// to ApplyDefaults rather than reporting an inversion against an unset field.
		if p.smallerValue <= 0 || p.largerValue <= 0 {
			continue
		}
		if p.smallerValue > p.largerValue {
			return fmt.Errorf(
				"infrastructure.nats.%s (%d) exceeds %s (%d): %s. Raise %s to at least "+
					"%d, or lower %s — and remember every ceiling here is reserved up "+
					"front, so raising one may also need a larger JetStream volume",
				p.smaller, p.smallerValue, p.larger, p.largerValue, p.why,
				p.larger, p.smallerValue, p.smaller)
		}
	}
	return nil
}

// Creates the default instance configuration
func NewDefaultInstanceConfiguration() *InstanceConfiguration {
	return &InstanceConfiguration{
		Infrastructure: InfrastructureConfiguration{
			Nats: NatsConfiguration{
				Hostname:           "dc-nats.dc-system",
				Port:               4222,
				StreamReplicas:     1,
				StreamMaxBytes:     DefaultStreamMaxBytes,
				StreamMaxBytesCold: DefaultStreamMaxBytesCold,
				StreamMaxMsgs:      DefaultStreamMaxMsgs,
				StreamMaxMsgSize:   DefaultStreamMaxMsgSize,
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
			DeviceState: DeviceStateConfiguration{
				Hostname: "dc-device-state.dc-system",
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

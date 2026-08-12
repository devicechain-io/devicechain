// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	nats "github.com/nats-io/nats.go"
)

// connzPageSize is how many connections one CONNZ page asks for. The broker's own
// default is 1024 and it TRUNCATES silently at whatever the limit is — `total` reports
// the real count while `connections` holds only a page — so paging is mandatory, not
// an optimisation. Asking for a round number of our own makes the paging path run on
// every instance rather than only on ones large enough to cross the broker's default,
// which is the difference between a code path that is tested in production and one
// that first executes on the largest fleet we have.
const connzPageSize = 512

// connzPageLimit bounds the paging loop. At the page size above this covers a quarter
// of a million connections on one broker node, which is far past anything the platform
// supports; it exists so a broker that reported a `total` it never satisfies cannot
// spin forever.
const connzPageLimit = 500

// statszReply is the subset of a $SYS.REQ.SERVER.PING reply this package reads: which
// server answered, and how many servers IT believes are in the cluster.
type statszReply struct {
	Server struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"server"`
	Stats struct {
		// ActiveServers is the responding server's own count of the cluster,
		// itself included. It is what makes completeness checkable at all: without
		// it, "three servers replied" and "three servers exist" are indistinguishable.
		ActiveServers int `json:"active_servers"`
	} `json:"statsz"`
}

// connzReply is the subset of a CONNZ reply this package reads.
type connzReply struct {
	Server struct {
		ID string `json:"id"`
	} `json:"server"`
	Data *struct {
		Total       int         `json:"total"`
		NumConns    int         `json:"num_connections"`
		Offset      int         `json:"offset"`
		Connections []connzConn `json:"connections"`
	} `json:"data"`
	Error *struct {
		Description string `json:"description"`
	} `json:"error"`
}

// connzConn is one connection in a CONNZ reply.
//
// 🔴 `mqtt_client` HERE, `client_id` IN AN ADVISORY. Same value, different key, and a
// decoder pointed at the advisory's key reads "" for every connection — which does not
// error, it just yields an inventory with no devices in it. An empty inventory is
// indistinguishable from an idle instance, and in the disconnect direction it would
// mean "every device is gone". Kept as its own type next to advisory for exactly that
// reason.
type connzConn struct {
	MQTTClient string     `json:"mqtt_client"`
	Type       string     `json:"type"`
	Start      *time.Time `json:"start"`
}

// LiveDevice is one device the broker currently holds a plain connection for.
type LiveDevice struct {
	Tenant      string
	DeviceToken string
	SessionId   uint64
}

// Inventory is the broker's account of who is connected right now.
type Inventory struct {
	// Devices is every plain device connection for this instance, across the cluster.
	Devices map[string]LiveDevice
	// Complete reports whether every server in the cluster answered.
	//
	// 🔴 THIS FLAG IS THE DIFFERENCE BETWEEN A REPAIR AND A MASS FALSE DEATH. The
	// inventory is a fan-out: each server describes only the connections attached to
	// itself, so one missing reply means every device on that node is absent from
	// Devices. Read as "connected devices" that is a delayed repair — the next pass
	// catches them. Read as "the complete set of connected devices", which is what the
	// disconnect direction needs, it says a third of the fleet just died.
	Complete bool
	// Servers is how many distinct servers answered, and Expected how many the cluster
	// says exist. Reported so an incomplete pass says how incomplete.
	Servers  int
	Expected int
}

// Requester is the broker access the inventory needs, as two operations rather than the
// five NATS calls they are built from.
//
// 🔑 Gather EXISTS BECAUSE A FAN-OUT IS NOT A REQUEST. It publishes and collects EVERY
// reply within a window, where Request returns the first and discards the rest. Exposing
// it as one operation also keeps *nats.Subscription — a concrete type nothing can
// substitute — out of the interface, which is what makes the whole fetch, including the
// completeness rule, drivable without a broker. An interface shaped around the NATS
// calls instead of around the operations left exactly one seam untestable, and that seam
// is the one that decides whether devices get marked offline.
type Requester interface {
	// RequestWithContext is an ordinary single-responder request.
	RequestWithContext(ctx context.Context, subject string, data []byte) (*nats.Msg, error)
	// Gather publishes to a fan-out subject and returns every reply that arrives within
	// the window.
	Gather(ctx context.Context, subject string, data []byte, window time.Duration) ([][]byte, error)
}

// natsRequester adapts *nats.Conn to Requester.
type natsRequester struct{ nc *nats.Conn }

// NewRequester wraps a live NATS connection.
func NewRequester(nc *nats.Conn) Requester { return &natsRequester{nc: nc} }

func (r *natsRequester) RequestWithContext(ctx context.Context, subject string, data []byte) (*nats.Msg, error) {
	return r.nc.RequestWithContext(ctx, subject, data)
}

func (r *natsRequester) Gather(ctx context.Context, subject string, data []byte, window time.Duration) ([][]byte, error) {
	inbox := r.nc.NewRespInbox()
	sub, err := r.nc.SubscribeSync(inbox)
	if err != nil {
		return nil, fmt.Errorf("subscribing for replies to %s: %w", subject, err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	// Flushed before publishing: an un-flushed subscription is only queued client-side,
	// so the request could reach the servers before the interest does and every reply
	// would be dropped — reading as a cluster where nobody answered.
	if err := r.nc.Flush(); err != nil {
		return nil, fmt.Errorf("flushing the reply subscription for %s: %w", subject, err)
	}
	if err := r.nc.PublishRequest(subject, inbox, data); err != nil {
		return nil, fmt.Errorf("publishing %s: %w", subject, err)
	}
	deadline := time.Now().Add(window)
	replies := make([][]byte, 0, 3)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 || ctx.Err() != nil {
			return replies, nil
		}
		msg, err := sub.NextMsg(remaining)
		if err != nil {
			return replies, nil // the window closed, which is the ordinary way out
		}
		replies = append(replies, msg.Data)
	}
}

// FetchInventory asks the broker who is connected.
//
// It is deliberately two phases. The first (a PING) establishes WHICH servers exist,
// because the second phase's completeness cannot be judged from its own replies — "two
// answered" tells you nothing without "there are three". The second then asks each
// server DIRECTLY rather than fanning out again, which turns paging back into ordinary
// request/reply against a single responder: a fan-out page-2 request would be answered
// by every server at once, with no way to attribute an offset to the server it belongs
// to.
func FetchInventory(ctx context.Context, r Requester, instanceId string, gather time.Duration) (Inventory, error) {
	servers, expected, err := pingServers(ctx, r, gather)
	if err != nil {
		return Inventory{}, err
	}
	inv := Inventory{
		Devices:  make(map[string]LiveDevice),
		Servers:  len(servers),
		Expected: expected,
		Complete: len(servers) > 0 && len(servers) >= expected,
	}
	for _, id := range servers {
		if err := collectServer(ctx, r, instanceId, id, inv.Devices); err != nil {
			// One unreachable server does not void the pass: the devices already
			// collected are still genuinely connected. It does void COMPLETENESS,
			// which is what the disconnect direction reads.
			inv.Complete = false
			continue
		}
	}
	return inv, nil
}

// pingServers collects one statsz reply per server within the gather window, returning
// the responding server ids and the cluster size they claim.
//
// The window is a real wait rather than a timeout on a single request: this is a
// fan-out, and nats.Request returns the FIRST reply and discards the rest. Using it
// here would inventory one server out of three and report the other two as having no
// connections.
func pingServers(ctx context.Context, r Requester, gather time.Duration) ([]string, int, error) {
	replies, err := r.Gather(ctx, sysServerPing, []byte("{}"), gather)
	if err != nil {
		return nil, 0, err
	}
	seen := make(map[string]bool)
	ids := make([]string, 0, 3)
	expected := 0
	for _, raw := range replies {
		reply := statszReply{}
		if json.Unmarshal(raw, &reply) != nil || reply.Server.ID == "" {
			continue
		}
		if !seen[reply.Server.ID] {
			seen[reply.Server.ID] = true
			ids = append(ids, reply.Server.ID)
		}
		// The MAXIMUM claim wins. A server that has just lost a route reports a
		// smaller cluster than the truth, and taking the smaller number would let a
		// partitioned view declare itself complete — which is the reading that turns
		// a network blip into a mass false death. (The high-water mark in Reconciler
		// closes the remaining case, where EVERY reachable server under-reports.)
		if reply.Stats.ActiveServers > expected {
			expected = reply.Stats.ActiveServers
		}
	}
	if len(ids) == 0 {
		return nil, 0, fmt.Errorf("no server answered %s within %s", sysServerPing, gather)
	}
	return ids, expected, nil
}

// collectServer pages one server's connections into devices.
func collectServer(ctx context.Context, r Requester, instanceId, serverID string, devices map[string]LiveDevice) error {
	subject := fmt.Sprintf(sysServerDirect, serverID, "CONNZ")
	offset := 0
	for page := 0; page < connzPageLimit; page++ {
		body, err := json.Marshal(map[string]any{"offset": offset, "limit": connzPageSize})
		if err != nil {
			return err
		}
		msg, err := r.RequestWithContext(ctx, subject, body)
		if err != nil {
			return fmt.Errorf("requesting connections from server %s: %w", serverID, err)
		}
		reply := connzReply{}
		if err := json.Unmarshal(msg.Data, &reply); err != nil {
			return fmt.Errorf("decoding the connection list from server %s: %w", serverID, err)
		}
		if reply.Error != nil {
			return fmt.Errorf("server %s refused the connection list: %s", serverID, reply.Error.Description)
		}
		if reply.Data == nil {
			return fmt.Errorf("server %s returned no connection data", serverID)
		}
		for _, c := range reply.Data.Connections {
			addLiveDevice(instanceId, c, devices)
		}
		offset += reply.Data.NumConns
		// Stop on a satisfied total, and also on a page that returned nothing — a
		// server whose `total` overstates what it will actually serve would otherwise
		// hold the loop until the page ceiling.
		if reply.Data.NumConns == 0 || offset >= reply.Data.Total {
			return nil
		}
	}
	return fmt.Errorf("server %s did not finish listing connections within %d pages", serverID, connzPageLimit)
}

// addLiveDevice records one connection if it is one of this instance's plain device
// sessions. The filter is deliberately identical to the advisory path's — same parse,
// same discriminator rule, same canary exclusion — because a device asserted by one and
// reconciled by the other must be the same set, or the two directions of reconciliation
// would disagree with each other about who exists.
//
// The canary exclusion matters here even though the tenant loop would usually mask it:
// the canary holds a real connection under a device-shaped id, so it appears in the
// inventory, and the masking is exactly what fails in the CanaryTenant-collision case
// the constant's own doc warns about.
func addLiveDevice(instanceId string, c connzConn, devices map[string]LiveDevice) {
	if c.MQTTClient == "" {
		return
	}
	parts, err := messaging.ParseDeviceClientIDFor(instanceId, c.MQTTClient)
	if err != nil || parts.Discriminator != "" || IsCanary(parts) {
		return
	}
	session, ok := SessionIDFrom(c.Start)
	if !ok {
		return
	}
	devices[DeviceKey(parts.Tenant, parts.DeviceToken)] = LiveDevice{
		Tenant:      parts.Tenant,
		DeviceToken: parts.DeviceToken,
		SessionId:   session,
	}
}

// DeviceKey is the map key a device is held under in an inventory or a projection
// snapshot. One helper so the two sides of a diff cannot key differently.
func DeviceKey(tenant, deviceToken string) string { return tenant + "/" + deviceToken }

const (
	// sysServerPing is the fan-out statsz request every server answers.
	sysServerPing = "$SYS.REQ.SERVER.PING"
	// sysServerDirect addresses ONE server's monitoring endpoint by id.
	sysServerDirect = "$SYS.REQ.SERVER.%s.%s"
)

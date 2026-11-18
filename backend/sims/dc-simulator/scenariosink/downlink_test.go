// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenariosink

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/devicechain-io/dc-simulator/lorawan"
	"github.com/devicechain-io/dc-simulator/sim"
)

func TestChirpStackQueueRecordsAndClassASnapshot(t *testing.T) {
	sink := New()
	server := httptest.NewServer(sink)
	defer server.Close()
	post := func(tenant, key, body string) int {
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/chirpstack/"+tenant+"/api/devices/"+sim.HarborpulseSamplingDevEUI+"/queue", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	body := fmt.Sprintf(`{"queueItem":{"devEui":%q,"fPort":12,"confirmed":true,"data":"AA8="}}`, sim.HarborpulseSamplingDevEUI)
	take := func(tenant string) []lorawan.PendingDownlink {
		req, _ := http.NewRequest(http.MethodGet, server.URL+"/chirpstack/"+tenant+"/pending/"+sim.HarborpulseSamplingDevEUI, nil)
		req.Header.Set("Authorization", "Bearer "+sim.HarborpulseQueueKey(tenant))
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var items []lorawan.PendingDownlink
		if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
			t.Fatal(err)
		}
		return items
	}
	if code := post("one", "wrong", body); code != 401 {
		t.Fatalf("wrong key HTTP %d", code)
	}
	if code := post("one", sim.HarborpulseQueueKey("one"), `{"queueItem":{}}`); code != 400 {
		t.Fatalf("invalid body HTTP %d", code)
	}
	if len(sink.Snapshot().QueueItems) != 0 {
		t.Fatal("refusal recorded as queued")
	}
	if got := take("one"); len(got) != 0 {
		t.Fatal("empty queue had pending items")
	}
	if code := post("one", sim.HarborpulseQueueKey("one"), body); code != 200 {
		t.Fatalf("queue HTTP %d", code)
	}
	if got := take("two"); len(got) != 0 {
		t.Fatal("tenant two consumed tenant one's item")
	}
	first := take("one")
	if len(first) != 1 || first[0].FCntDown != 0 || first[0].Data != "AA8=" || first[0].FPort != 12 {
		t.Fatalf("snapshot %+v", first)
	}
	// This arrives after the pre-uplink snapshot: it waits for the following uplink.
	if code := post("one", sim.HarborpulseQueueKey("one"), body); code != 200 {
		t.Fatal(code)
	}
	if len(first) != 1 {
		t.Fatal("snapshot changed during an uplink")
	}
	second := take("one")
	if len(second) != 1 || second[0].FCntDown != 1 || second[0].ID == first[0].ID {
		t.Fatalf("second snapshot %+v", second)
	}
	if len(take("one")) != 0 {
		t.Fatal("item delivered twice")
	}
	rec := sink.Snapshot()
	if len(rec.QueueItems) != 2 || rec.QueueItems[0].ID != first[0].ID || rec.QueueItems[0].Hex != "000f" || rec.QueueItems[0].Tenant != "one" || !rec.QueueItems[0].Confirmed {
		t.Fatalf("queue record %+v", rec.QueueItems)
	}
}

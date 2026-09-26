// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// `dcctl sim create` is idempotent because the admin mutations it wraps tolerate the
// server's CONFLICT refusal. These drive the REAL Admin over HTTP against a stub that
// answers login and then refuses the admin mutation with the given errors array.
//
// Login and the admin mutation share one URL, so the stub routes on the request body:
// a document containing `login(` is the login; anything else is the admin mutation.
//
// The code is spelled as the literal "CONFLICT", the wire string, so the test pins it.

func adminStub(t *testing.T, adminErrors string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(body.Query, "login(") {
			_, _ = fmt.Fprintf(w, `{"data":{"login":{"identityToken":"t","expiresAt":%q,"superuser":true,"memberships":[]}}}`,
				time.Now().Add(time.Hour).Format(time.RFC3339))
			return
		}
		_, _ = fmt.Fprintf(w, `{"errors":%s}`, adminErrors)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func addTenantAdmin(t *testing.T, adminErrors string) error {
	t.Helper()
	srv := adminStub(t, adminErrors)
	return NewAdmin(srv.URL, srv.URL, "admin@example.com", "pw").
		AddTenantAdmin(context.Background(), "sim@example.com", "sim-a")
}

// 🔴 THE DEFECT. A re-run of `sim create` reaches addMembership for a membership that
// already exists. The server's refusal ("identity already has a membership in this
// tenant") used none of the phrases the old matcher looked for, so the re-run failed
// here. Tolerated by its code, it succeeds.
func TestAnExistingMembershipIsToleratedByItsCode(t *testing.T) {
	err := addTenantAdmin(t,
		`[{"message":"identity already has a membership in this tenant","extensions":{"code":"CONFLICT"}}]`)
	if err != nil {
		t.Fatalf("a CONFLICT refusal was not tolerated, so a re-run of sim create stops here: %v", err)
	}
}

// The controls: what must NOT be tolerated. Each is a real failure, and each must come
// back as an error that still carries its cause.
func TestOnlyAnAllConflictRefusalIsTolerated(t *testing.T) {
	for name, errs := range map[string]string{
		// A deleted tenant's reserved token, which the server refuses WITHOUT the code:
		// carrying on would mint access to a tenant nobody can enter.
		"the reservation, uncoded": `[{"message":"create tenant \"sim-a\": that tenant token is reserved: ` +
			`a tenant at this token is being deleted"}]`,
		// The cutover: prose no longer counts, however it reads.
		"an already-exists in prose only":  `[{"message":"tenant already exists"}]`,
		"a unique violation in prose only": `[{"message":"duplicate key value violates unique constraint \"x\""}]`,
		"another code":                     `[{"message":"slow down","extensions":{"code":"THROTTLED"}}]`,
		// One CONFLICT beside an uncoded real failure is not benign.
		"a conflict beside a real failure": `[{"message":"m","extensions":{"code":"CONFLICT"}},{"message":"permission denied"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			err := addTenantAdmin(t, errs)
			if err == nil {
				t.Fatalf("🔴 a real refusal was tolerated as success: %s", errs)
			}
			if !strings.Contains(err.Error(), "add tenant-admin membership") {
				t.Fatalf("the returned error lost its context: %v", err)
			}
			var refusal []struct {
				Message string `json:"message"`
			}
			if jerr := json.Unmarshal([]byte(errs), &refusal); jerr != nil || len(refusal) == 0 {
				t.Fatalf("bad fixture %s: %v", errs, jerr)
			}
			if !strings.Contains(err.Error(), refusal[0].Message) {
				t.Fatalf("the returned error dropped the server's refusal %q: %v", refusal[0].Message, err)
			}
		})
	}
}

// A nil inner error is success and must stay success — without this, every control
// above would pass just as happily against a tolerateExists that rejected everything.
func TestTolerateExistsKeepsASuccess(t *testing.T) {
	if got := tolerateExists(errors.New("wrapped"), nil); got != nil {
		t.Fatalf("tolerateExists turned a success into an error: %v", got)
	}
}

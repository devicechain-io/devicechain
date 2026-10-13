// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// A revision number names a revision OF THIS DEVICE. Another device's revision with the
// same number is not found, and nothing is written.
func TestRestoringAnotherDevicesRevisionNumberIsNotFound(t *testing.T) {
	api, ctx := configTestApi(t)
	seedDeclaredDevice(t, api, ctx, "prof", "dev", restoreDeclaration)
	if _, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "other", DeviceTypeToken: "prof-type"}); err != nil {
		t.Fatal(err)
	}
	setShared(t, api, ctx, "dev", "interval", AttributeValueLong, "1")
	setShared(t, api, ctx, "other", "interval", AttributeValueLong, "7")
	setShared(t, api, ctx, "other", "interval", AttributeValueLong, "8")
	before := desiredOf(t, api, ctx, "dev")

	if _, err := api.RestoreDeviceConfiguration(ctx, "dev", 2); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("restoring revision 2, which only another device has: %v, want not found", err)
	}
	if after := desiredOf(t, api, ctx, "dev"); after.Revision != before.Revision || after.Document != before.Document {
		t.Fatalf("the device took another device's revision: %+v", after)
	}
}

// Restoring the content the device already holds delivers nothing: no revision was minted,
// so there is nothing new to tell the device.
func TestRestoringTheCurrentContentDeliversNothing(t *testing.T) {
	api, ctx := configTestApi(t)
	pub := &recordingDesired{}
	api.DeviceDesiredPublisher = pub
	seedDeclaredDevice(t, api, ctx, "prof", "dev", restoreDeclaration)
	setShared(t, api, ctx, "dev", "interval", AttributeValueLong, "30")
	current := desiredOf(t, api, ctx, "dev")
	sent := len(pub.snapshot())

	if _, err := api.RestoreDeviceConfiguration(ctx, "dev", current.Revision); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := pub.snapshot(); len(got) != sent {
		t.Fatalf("a restore that minted nothing delivered %+v", got[sent:])
	}
}

// A value that does not survive the round trip into the type now declared is refused with
// the value-type code — not written as a nearby value, and not left to surface as an
// uncoded internal error. 9007199254740993 is a LONG no float64 can hold.
func TestRestoringALongIntoADoubleThatLosesPrecisionIsRefused(t *testing.T) {
	api, ctx := configTestApi(t)
	seedDeclaredDevice(t, api, ctx, "prof", "dev", []ConfigurationKey{{Key: "n", ValueType: "LONG"}})
	setShared(t, api, ctx, "dev", "n", AttributeValueLong, "9007199254740993")
	old := desiredOf(t, api, ctx, "dev")
	if old.Document != `{"n":9007199254740993}` {
		t.Fatalf("seed document %s", old.Document)
	}
	if _, err := api.SetDeviceProfileConfigurationDeclaration(ctx, "prof", []ConfigurationKey{{Key: "n", ValueType: "DOUBLE"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester"); err != nil {
		t.Fatal(err)
	}
	n := len(revisionsOf(t, api, ctx, "dev"))
	_, err := api.RestoreDeviceConfiguration(ctx, "dev", old.Revision)
	if got := refusalCode(err); got != CodeConfigurationValueType {
		t.Fatalf("refusal code %q (%v), want %q", got, err, CodeConfigurationValueType)
	}
	if len(revisionsOf(t, api, ctx, "dev")) != n {
		t.Fatal("a refused restore minted a revision")
	}
}

// The byte check is what stops a restore minting a document that is not the one asked
// for. A declared key written into the transaction between the attribute writes and the
// document build (the shape of a concurrent writer that commits first) must refuse the
// restore with the retryable conflict code, roll everything back, and mint nothing.
func TestARestoreWhoseWrittenDocumentDiffersIsAConflictAndMintsNothing(t *testing.T) {
	api, ctx := configTestApi(t)
	seedDeclaredDevice(t, api, ctx, "prof", "dev", []ConfigurationKey{
		{Key: "a", ValueType: "LONG"}, {Key: "z", ValueType: "LONG"},
	})
	setShared(t, api, ctx, "dev", "z", AttributeValueLong, "1")
	target := desiredOf(t, api, ctx, "dev") // {"z":1}
	setShared(t, api, ctx, "dev", "z", AttributeValueLong, "2")
	n := len(revisionsOf(t, api, ctx, "dev"))

	devId, err := api.deviceIdByToken(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	armed := true
	db := api.RDB.Database
	if err := db.Callback().Update().After("gorm:update").Register("test:interleave", func(d *gorm.DB) {
		row, ok := d.Statement.Dest.(*EntityAttribute)
		if !armed || !ok || row.AttrKey != "z" {
			return
		}
		armed = false
		seven := "7"
		_ = d.Session(&gorm.Session{NewDB: true}).Create(&EntityAttribute{
			EntityType: string(entity.TypeDevice), EntityId: devId, Scope: string(AttributeScopeShared),
			AttrKey: "a", ValueType: string(AttributeValueLong), Value: rdb.NullStrOf(&seven),
		}).Error
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove("test:interleave") })

	_, err = api.RestoreDeviceConfiguration(ctx, "dev", target.Revision)
	if armed {
		t.Fatal("the interleaving write never ran; the test proves nothing")
	}
	if got := refusalCode(err); got != CodeConfigurationConflict {
		t.Fatalf("refusal code %q (%v), want %q", got, err, CodeConfigurationConflict)
	}
	if got := revisionsOf(t, api, ctx, "dev"); len(got) != n {
		t.Fatalf("a refused restore minted %+v", got[0])
	}
	if got := desiredOf(t, api, ctx, "dev").Document; got != `{"z":2}` {
		t.Fatalf("the refused restore left %s behind", got)
	}
}

// A restore is an attribute write, so it moves the device between dynamic groups exactly
// as the attribute write it replaces would, in the same transaction.
func TestRestoringRecomputesGroupMembership(t *testing.T) {
	api, ctx := configTestApi(t)
	seedDeclaredDevice(t, api, ctx, "prof", "dev", []ConfigurationKey{{Key: "climate", ValueType: "STRING"}})
	setShared(t, api, ctx, "dev", "climate", AttributeValueString, "arid")
	arid := desiredOf(t, api, ctx, "dev")
	setShared(t, api, ctx, "dev", "climate", AttributeValueString, "humid")

	g, v := publishAridGroup(t, api, ctx, "arid-devices")
	if err := api.RegisterGroupVersionForScoping(ctx, "arid-devices", v); err != nil {
		t.Fatalf("register: %v", err)
	}
	devId, err := api.deviceIdByToken(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if isMemberOf(t, api, ctx, "device", devId, g.ID, v) {
		t.Fatal("a humid device starts as a member")
	}
	if _, err := api.RestoreDeviceConfiguration(ctx, "dev", arid.Revision); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !isMemberOf(t, api, ctx, "device", devId, g.ID, v) {
		t.Fatal("restoring climate=arid did not move the device into the arid group")
	}
}

// committedCheckingDesired records, for each delivery, whether a reader outside the
// restore's transaction could already see the delivered revision.
type committedCheckingDesired struct {
	api     *Api
	visible []bool
}

// The read runs on its own goroutine with a deadline: a publish from INSIDE the transaction
// would otherwise block on the transaction's own lock forever, and a hang is not a verdict.
func (c *committedCheckingDesired) PublishDesiredConfiguration(ctx context.Context, token string, rev *DeviceConfigurationRevision) {
	seen := make(chan bool, 1)
	go func() {
		var rows []DeviceConfigurationRevision
		err := c.api.RDB.DB(ctx).Where("device_id = ? AND revision = ?", rev.DeviceId, rev.Revision).Find(&rows).Error
		seen <- err == nil && len(rows) == 1
	}()
	select {
	case ok := <-seen:
		c.visible = append(c.visible, ok)
	case <-time.After(2 * time.Second):
		c.visible = append(c.visible, false)
	}
}

// Delivery happens after commit: when the device is told about a revision, a reader outside
// the restore's transaction can already see it.
func TestARestoredRevisionIsDeliveredOnlyAfterItCommits(t *testing.T) {
	api, ctx := configTestApi(t)
	seedDeclaredDevice(t, api, ctx, "prof", "dev", restoreDeclaration)
	setShared(t, api, ctx, "dev", "interval", AttributeValueLong, "30")
	target := desiredOf(t, api, ctx, "dev")
	setShared(t, api, ctx, "dev", "interval", AttributeValueLong, "60")

	pub := &committedCheckingDesired{api: api}
	api.DeviceDesiredPublisher = pub
	if _, err := api.RestoreDeviceConfiguration(ctx, "dev", target.Revision); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(pub.visible) != 1 || !pub.visible[0] {
		t.Fatalf("delivered %d time(s), committed-visible %v; want once, after commit", len(pub.visible), pub.visible)
	}
}

// A copy that names the source revision applies THAT revision even when the source has
// moved on since: what an operator previewed is what is applied. Without it the copy takes
// the source's latest. A revision the source does not have is not found.
func TestCopyingANamedSourceRevisionAppliesThatRevision(t *testing.T) {
	api, ctx := configTestApi(t)
	seedDeclaredDevice(t, api, ctx, "prof", "src", restoreDeclaration)
	if _, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "dst", DeviceTypeToken: "prof-type"}); err != nil {
		t.Fatal(err)
	}
	setShared(t, api, ctx, "src", "interval", AttributeValueLong, "45")
	previewed := desiredOf(t, api, ctx, "src")
	setShared(t, api, ctx, "src", "interval", AttributeValueLong, "90") // the source moves on
	latest := desiredOf(t, api, ctx, "src")

	cfg, err := api.CopyDeviceConfiguration(ctx, "src", "dst", &previewed.Revision)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if cfg.Desired == nil || cfg.Desired.Document != previewed.Document {
		t.Fatalf("copied %+v, want the previewed %s (not the latest %s)", cfg.Desired, previewed.Document, latest.Document)
	}

	missing := latest.Revision + 10
	if _, err := api.CopyDeviceConfiguration(ctx, "src", "dst", &missing); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("copying a revision the source does not have: %v, want not found", err)
	}
}

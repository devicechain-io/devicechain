// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// ErrNoPending is a definite absence: no downlink awaiting an acknowledgement matches. Any other
// error from a PendingStore is a transient fault.
var ErrNoPending = errors.New("lpwan: no pending downlink matches")

// Pending is a CONFIRMED downlink the network server accepted and the device has not yet
// acknowledged. It is the only memory of the downlink: the command itself is SENT in
// command-delivery, and settles when this record's report (or deadline) answers it.
type Pending struct {
	ID     uint
	Tenant string
	// Integration is the inbound integration the downlink was sent through. A report only matches
	// a downlink from the integration its token proved.
	Integration string
	// DeviceToken is the command's device (the command-responses subject names it).
	DeviceToken string
	// DevEUI is the device the downlink was queued for, canonical upper-case hex.
	DevEUI string
	// CommandToken and DispatchNonce are the command and the CLAIM's nonce: command-delivery
	// refuses an answer that does not quote it.
	CommandToken  string
	DispatchNonce string
	// QueueItemID is the network server's id for the queued downlink.
	QueueItemID string
	// DeadlineAt is when the downlink stops waiting for its acknowledgement and the command is
	// answered as unanswered.
	DeadlineAt time.Time
	// TransmittedAt is when a gateway accepted the frame for transmission (a txack), if one was
	// reported.
	TransmittedAt *time.Time
}

// SettleLease is how long a settler (a report or the deadline sweep) holds a record while it
// answers the command. It is shorter than the report stream's redelivery wait (60 s), so a report
// whose answer failed can claim the record again when it redelivers, and it bounds how long a
// settler that died mid-answer blocks the record.
const SettleLease = 45 * time.Second

// PendingStore holds the downlinks awaiting an acknowledgement.
type PendingStore interface {
	// Add records a pending downlink. The (tenant, queue item id) pair is unique.
	Add(ctx context.Context, p Pending) error
	// Lookup returns the pending downlink with the queue item id, or ErrNoPending.
	Lookup(ctx context.Context, tenant, queueItemID string) (*Pending, error)
	// MarkTransmitted records the gateway's acceptance of the frame, keeping the first.
	MarkTransmitted(ctx context.Context, tenant string, id uint, at time.Time) error
	// Claim takes the record's settle lease for lease from now, reporting whether this caller got it.
	// Exactly one of a report and the deadline sweep settles a record, so the command is never
	// answered both SUCCESSFUL and TIMEOUT: whoever claims first publishes its outcome, and the
	// other leaves the record alone.
	Claim(ctx context.Context, tenant string, id uint, now time.Time, lease time.Duration) (bool, error)
	// Delete removes a settled record. Deleting one already gone is not an error.
	Delete(ctx context.Context, tenant string, id uint) error
	// Expired returns up to limit records whose deadline is at or before now and that no settler
	// holds, across tenants.
	Expired(ctx context.Context, now time.Time, limit int) ([]Pending, error)
}

// LpwanPendingAck is the table row behind Pending (lpwan_pending_acks). The migration that creates
// it declares its own snapshot (schema.lpwanPendingAck); this is the live shape. It has no
// TableName: gorm derives the name, and only a derived name takes the area's table prefix.
type LpwanPendingAck struct {
	ID        uint `gorm:"primaryKey"`
	CreatedAt time.Time
	// (tenant_id, queue_item_id) is unique: the network server's id names one downlink, and a
	// report is only ever looked up inside its own tenant.
	TenantId      string    `gorm:"not null;size:128;uniqueIndex:uix_lpwan_pending_acks_item,priority:1"`
	Integration   string    `gorm:"not null;size:128"`
	DeviceToken   string    `gorm:"not null;size:128"`
	DevEUI        string    `gorm:"column:dev_eui;not null;size:16"`
	CommandToken  string    `gorm:"not null;size:128"`
	DispatchNonce string    `gorm:"not null;size:128"`
	QueueItemID   string    `gorm:"not null;size:64;uniqueIndex:uix_lpwan_pending_acks_item,priority:2"`
	DeadlineAt    time.Time `gorm:"not null;index"`
	TransmittedAt *time.Time
	SettlingUntil *time.Time
}

// DBPending is the relational PendingStore.
type DBPending struct{ rdb *rdb.RdbManager }

// NewDBPending builds the store over the service database.
func NewDBPending(m *rdb.RdbManager) *DBPending { return &DBPending{rdb: m} }

func (s *DBPending) db(ctx context.Context, tenant string) *gorm.DB {
	return s.rdb.DB(core.WithTenant(ctx, tenant))
}

func fromRow(r LpwanPendingAck) Pending {
	return Pending{ID: r.ID, Tenant: r.TenantId, Integration: r.Integration, DeviceToken: r.DeviceToken,
		DevEUI: r.DevEUI, CommandToken: r.CommandToken, DispatchNonce: r.DispatchNonce,
		QueueItemID: r.QueueItemID, DeadlineAt: r.DeadlineAt, TransmittedAt: r.TransmittedAt}
}

// Add implements PendingStore.
func (s *DBPending) Add(ctx context.Context, p Pending) error {
	row := LpwanPendingAck{TenantId: p.Tenant, Integration: p.Integration, DeviceToken: p.DeviceToken, DevEUI: p.DevEUI,
		CommandToken: p.CommandToken, DispatchNonce: p.DispatchNonce, QueueItemID: p.QueueItemID,
		DeadlineAt: p.DeadlineAt.UTC()}
	return s.db(ctx, p.Tenant).Create(&row).Error
}

// Lookup implements PendingStore.
func (s *DBPending) Lookup(ctx context.Context, tenant, queueItemID string) (*Pending, error) {
	var row LpwanPendingAck
	err := s.db(ctx, tenant).Where("queue_item_id = ?", queueItemID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNoPending
	}
	if err != nil {
		return nil, err
	}
	p := fromRow(row)
	return &p, nil
}

// MarkTransmitted implements PendingStore. The first report wins.
func (s *DBPending) MarkTransmitted(ctx context.Context, tenant string, id uint, at time.Time) error {
	return s.db(ctx, tenant).Model(&LpwanPendingAck{}).
		Where("id = ? AND transmitted_at IS NULL", id).
		Update("transmitted_at", at.UTC()).Error
}

// Claim implements PendingStore. The conditional update is the atomic step.
func (s *DBPending) Claim(ctx context.Context, tenant string, id uint, now time.Time, lease time.Duration) (bool, error) {
	res := s.db(ctx, tenant).Model(&LpwanPendingAck{}).
		Where("id = ? AND (settling_until IS NULL OR settling_until <= ?)", id, now.UTC()).
		Update("settling_until", now.Add(lease).UTC())
	return res.RowsAffected == 1, res.Error
}

// Delete implements PendingStore.
func (s *DBPending) Delete(ctx context.Context, tenant string, id uint) error {
	return s.db(ctx, tenant).Where("id = ?", id).Delete(&LpwanPendingAck{}).Error
}

// Expired implements PendingStore. It is the one cross-tenant read: the sweep answers every
// tenant's overdue downlinks, so it runs under the system context and answers each in its own.
func (s *DBPending) Expired(ctx context.Context, now time.Time, limit int) ([]Pending, error) {
	var rows []LpwanPendingAck
	err := s.rdb.DB(core.WithSystemContext(ctx)).
		Where("deadline_at <= ? AND (settling_until IS NULL OR settling_until <= ?)", now.UTC(), now.UTC()).Order("deadline_at ASC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]Pending, len(rows))
	for i, r := range rows {
		out[i] = fromRow(r)
	}
	return out, nil
}

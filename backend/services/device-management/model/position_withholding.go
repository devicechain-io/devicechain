// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"reflect"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Position withholding: an alarm a geofence rule raised (Alarm.PositionDerived) is not read by a
// caller without location:read.
//
// Such an alarm is a position report in all but name. It is keyed by device, it goes ACTIVE when
// the device enters (or leaves) the fence and CLEARED when it leaves (or enters) it, and both
// instants are on the row. The live detections of the same rule are withheld from a caller
// without location:read (event-processing's detectionStream), and so is the rule preview; the
// alarm, which is those detections integrated, is withheld on the same terms. Whole alarms are
// withheld rather than fields redacted, because no field can be redacted that does not leave the
// alarm's existence, its originator and its times, which are the position.
//
// It is a gorm callback rather than a predicate at each call site because every alarm read
// (the queries, the counts, the token and id lookups behind acknowledge, clear and label, the
// live stream's per-event check, the agent tools) has to agree, and a call site that forgot the
// predicate would answer with the alarm. The callback acts only on a statement whose context
// carries a caller (auth claims) that lacks location:read. A context with no claims (the
// raise-alarm consumer, every background worker) and a system context are untouched, so the
// integrator always sees the alarm it folds into.
//
// It covers two tables: alarms, and alarm_dispositions, whose rows name a cycle's raise time
// and would otherwise list the cycles of a withheld alarm. Like the scope callback it does not
// cover a raw Exec or a JOINed table; no alarm read uses either.

// RegisterPositionWithholding installs the callback on db's query and row processors. The
// service calls it once on its database handle, after the tenant scoping is registered.
//
// Registering it again on the same handle is a no-op, so a test that builds the Apis twice over
// one database does not install it twice.
func RegisterPositionWithholding(db *gorm.DB) error {
	if db.Callback().Query().Get("dm:position_withholding_query") != nil {
		return nil
	}
	if err := db.Callback().Query().Before("gorm:query").Register("dm:position_withholding_query", withholdPositionDerived); err != nil {
		return err
	}
	return db.Callback().Row().Before("gorm:row").Register("dm:position_withholding_row", withholdPositionDerived)
}

var (
	alarmModelType            = reflect.TypeOf(Alarm{})
	alarmDispositionModelType = reflect.TypeOf(AlarmDisposition{})
)

// CallerHoldsLocationRead reports whether the context's caller holds location:read. It asks
// through AuthorizeScoped, not Authorize: a member limited to some customers may read alarms
// (within its scope), and Authorize refuses every such member whatever it holds, which would
// withhold geofence alarms from a scoped member that was granted location:read.
func CallerHoldsLocationRead(ctx context.Context) bool {
	_, err := auth.AuthorizeScoped(ctx, auth.LocationRead)
	return err == nil
}

// callerWithoutLocation reports whether the statement runs for a caller that lacks
// location:read. A context with no claims is not a caller.
func callerWithoutLocation(db *gorm.DB) bool {
	ctx := db.Statement.Context
	if ctx == nil || core.IsSystemContext(ctx) {
		return false
	}
	if _, ok := auth.ClaimsFromContext(ctx); !ok {
		return false
	}
	return !CallerHoldsLocationRead(ctx)
}

func withholdPositionDerived(db *gorm.DB) {
	if db.Error != nil || db.Statement.Schema == nil {
		return
	}
	typ := db.Statement.Schema.ModelType
	if typ != alarmModelType && typ != alarmDispositionModelType {
		return
	}
	if !callerWithoutLocation(db) {
		return
	}
	if db.Statement.SQL.Len() > 0 {
		// SQL already written (Raw) cannot take a predicate; refuse rather than answer unfiltered.
		_ = db.AddError(fmt.Errorf("%w (an alarm read whose SQL was already written, so position-derived alarms cannot be withheld)",
			auth.ErrForbidden))
		return
	}
	if typ == alarmModelType {
		// Only an alarm classified NOT position-derived is read. NULL is "not classified yet"
		// (position_classify.go) and is withheld like true: fail closed.
		db.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Expr{
			SQL:  "? IS FALSE",
			Vars: []any{clause.Column{Table: clause.CurrentTable, Name: "position_derived"}},
		}}})
		return
	}
	// A label's row names its alarm's id; leave out the labels of a withheld alarm. The alarms
	// table is named through the NamingStrategy, which carries the area's schema prefix.
	db.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Expr{
		SQL: "NOT EXISTS (SELECT 1 FROM ? AS withheld WHERE withheld.id = ? AND withheld.position_derived IS NOT FALSE)",
		Vars: []any{clause.Table{Name: db.NamingStrategy.TableName("Alarm")},
			clause.Column{Table: clause.CurrentTable, Name: "alarm_id"}},
	}}})
}

// AlarmPositionDerived returns the alarm's position classification (nil while it is not classified
// yet; see position_classify.go), and whether the alarm exists at all. It is the live alarm
// stream's question about an event it is about to push to a caller without location:read, so it
// is asked WITHOUT the caller's claims: the stream wants the alarm's flag, not the caller's view of
// it (which would also fold in the caller's scope, already judged on the event's own stamp).
func (api *Api) AlarmPositionDerived(ctx context.Context, token string) (derived *bool, found bool, err error) {
	var rows []Alarm
	if err := api.RDB.DB(auth.WithClaims(ctx, nil)).Select("position_derived").
		Where("token = ?", token).Limit(1).Find(&rows).Error; err != nil {
		return nil, false, err
	}
	if len(rows) == 0 {
		return nil, false, nil
	}
	return rows[0].PositionDerived, true, nil
}

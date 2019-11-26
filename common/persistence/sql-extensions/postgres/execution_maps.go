// Copyright (c) 2019 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package postgres

import (
	"database/sql"
	"fmt"
	"github.com/uber/cadence/common/persistence/sql/storage/sqldb"
	"strings"
)

const (
	deleteMapQueryTemplate = `DELETE FROM %v
WHERE
shard_id = $1 AND
domain_id = $2 AND
workflow_id = $3 AND
run_id = $4`

	// %[2]v is the columns of the value struct (i.e. no primary key columns), comma separated
	// %[3]v should be %[2]v with colons prepended.
	// i.e. %[3]v = ",".join(":" + s for s in %[2]v)
	// So that this query can be used with BindNamed
	// %[4]v should be the name of the key associated with the map
	// e.g. for ActivityInfo it is "schedule_id"
	setKeyInMapQueryTemplate = `INSERT INTO %[1]v
(shard_id, domain_id, workflow_id, run_id, %[4]v, %[2]v)
VALUES
(:shard_id, :domain_id, :workflow_id, :run_id, :%[4]v, %[3]v)
ON CONFLICT (shard_id, domain_id, workflow_id, run_id, %[4]v) DO UPDATE 
  SET shard_id = excluded.shard_id,
      domain_id = excluded.domain_id,
      workflow_id = excluded.workflow_id,
	  run_id = excluded.run_id,
      %[4]v = excluded.%[4]v `

	// %[2]v is the name of the key
	deleteKeyInMapQueryTemplate = `DELETE FROM %[1]v
WHERE
shard_id = $1 AND
domain_id = $2 AND
workflow_id = $3 AND
run_id = $4 AND
%[2]v = $5`

	// %[1]v is the name of the table
	// %[2]v is the name of the key
	// %[3]v is the value columns, separated by commas
	getMapQueryTemplate = `SELECT %[2]v, %[3]v FROM %[1]v
WHERE
shard_id = $1 AND
domain_id = $2 AND
workflow_id = $3 AND
run_id = $4`
)

const (
	deleteAllSignalsRequestedSetQuery = `DELETE FROM signals_requested_sets
WHERE
shard_id = $1 AND
domain_id = $2 AND
workflow_id = $3 AND
run_id = $4
`

	createSignalsRequestedSetQuery = `INSERT INTO signals_requested_sets
(shard_id, domain_id, workflow_id, run_id, signal_id) VALUES
(:shard_id, :domain_id, :workflow_id, :run_id, :signal_id)
ON CONFLICT (shard_id, domain_id, workflow_id, run_id, signal_id) DO NOTHING`

	deleteSignalsRequestedSetQuery = `DELETE FROM signals_requested_sets
WHERE
shard_id = $1 AND
domain_id = $2 AND
workflow_id = $3 AND
run_id = $4 AND
signal_id = $5`

	getSignalsRequestedSetQuery = `SELECT signal_id FROM signals_requested_sets WHERE
shard_id = $1 AND
domain_id = $2 AND
workflow_id = $3 AND
run_id = $4`
)



func stringMap(a []string, f func(string) string) []string {
	b := make([]string, len(a))
	for i, v := range a {
		b[i] = f(v)
	}
	return b
}

func makeDeleteMapQry(tableName string) string {
	return fmt.Sprintf(deleteMapQueryTemplate, tableName)
}

func makeSetKeyInMapQry(tableName string, nonPrimaryKeyColumns []string, mapKeyName string) string {
	return fmt.Sprintf(setKeyInMapQueryTemplate,
		tableName,
		strings.Join(nonPrimaryKeyColumns, ","),
		strings.Join(stringMap(nonPrimaryKeyColumns, func(x string) string {
			return ":" + x
		}), ","),
		mapKeyName)
}

func makeDeleteKeyInMapQry(tableName string, mapKeyName string) string {
	return fmt.Sprintf(deleteKeyInMapQueryTemplate,
		tableName,
		mapKeyName)
}

func makeGetMapQryTemplate(tableName string, nonPrimaryKeyColumns []string, mapKeyName string) string {
	return fmt.Sprintf(getMapQueryTemplate,
		tableName,
		mapKeyName,
		strings.Join(nonPrimaryKeyColumns, ","))
}

var (
	// Omit shard_id, run_id, domain_id, workflow_id, schedule_id since they're in the primary key
	activityInfoColumns = []string{
		"data",
		"data_encoding",
		"last_heartbeat_details",
		"last_heartbeat_updated_time",
	}
	activityInfoTableName = "activity_info_maps"
	activityInfoKey       = "schedule_id"

	deleteActivityInfoMapQry      = makeDeleteMapQry(activityInfoTableName)
	setKeyInActivityInfoMapQry    = makeSetKeyInMapQry(activityInfoTableName, activityInfoColumns, activityInfoKey)
	deleteKeyInActivityInfoMapQry = makeDeleteKeyInMapQry(activityInfoTableName, activityInfoKey)
	getActivityInfoMapQry         = makeGetMapQryTemplate(activityInfoTableName, activityInfoColumns, activityInfoKey)
)

// ReplaceIntoActivityInfoMaps replaces one or more rows in activity_info_maps table
func (mdb *db) ReplaceIntoActivityInfoMaps(rows []sqldb.ActivityInfoMapsRow) (sql.Result, error) {
	for i := range rows {
		rows[i].LastHeartbeatUpdatedTime = mdb.converter.ToMySQLDateTime(rows[i].LastHeartbeatUpdatedTime)
	}
	return mdb.conn.NamedExec(setKeyInActivityInfoMapQry, rows)
}

// SelectFromActivityInfoMaps reads one or more rows from activity_info_maps table
func (mdb *db) SelectFromActivityInfoMaps(filter *sqldb.ActivityInfoMapsFilter) ([]sqldb.ActivityInfoMapsRow, error) {
	var rows []sqldb.ActivityInfoMapsRow
	err := mdb.conn.Select(&rows, getActivityInfoMapQry, filter.ShardID, filter.DomainID, filter.WorkflowID, filter.RunID)
	for i := 0; i < len(rows); i++ {
		rows[i].ShardID = int64(filter.ShardID)
		rows[i].DomainID = filter.DomainID
		rows[i].WorkflowID = filter.WorkflowID
		rows[i].RunID = filter.RunID
		rows[i].LastHeartbeatUpdatedTime = mdb.converter.FromMySQLDateTime(rows[i].LastHeartbeatUpdatedTime)
	}
	return rows, err
}

// DeleteFromActivityInfoMaps deletes one or more rows from activity_info_maps table
func (mdb *db) DeleteFromActivityInfoMaps(filter *sqldb.ActivityInfoMapsFilter) (sql.Result, error) {
	if filter.ScheduleID != nil {
		return mdb.conn.Exec(deleteKeyInActivityInfoMapQry, filter.ShardID, filter.DomainID, filter.WorkflowID, filter.RunID, *filter.ScheduleID)
	}
	return mdb.conn.Exec(deleteActivityInfoMapQry, filter.ShardID, filter.DomainID, filter.WorkflowID, filter.RunID)
}

var (
	timerInfoColumns = []string{
		"data",
		"data_encoding",
	}
	timerInfoTableName = "timer_info_maps"
	timerInfoKey       = "timer_id"

	deleteTimerInfoMapSQLQuery      = makeDeleteMapQry(timerInfoTableName)
	setKeyInTimerInfoMapSQLQuery    = makeSetKeyInMapQry(timerInfoTableName, timerInfoColumns, timerInfoKey)
	deleteKeyInTimerInfoMapSQLQuery = makeDeleteKeyInMapQry(timerInfoTableName, timerInfoKey)
	getTimerInfoMapSQLQuery         = makeGetMapQryTemplate(timerInfoTableName, timerInfoColumns, timerInfoKey)
)

// ReplaceIntoTimerInfoMaps replaces one or more rows in timer_info_maps table
func (mdb *db) ReplaceIntoTimerInfoMaps(rows []sqldb.TimerInfoMapsRow) (sql.Result, error) {
	return mdb.conn.NamedExec(setKeyInTimerInfoMapSQLQuery, rows)
}

// SelectFromTimerInfoMaps reads one or more rows from timer_info_maps table
func (mdb *db) SelectFromTimerInfoMaps(filter *sqldb.TimerInfoMapsFilter) ([]sqldb.TimerInfoMapsRow, error) {
	var rows []sqldb.TimerInfoMapsRow
	err := mdb.conn.Select(&rows, getTimerInfoMapSQLQuery, filter.ShardID, filter.DomainID, filter.WorkflowID, filter.RunID)
	for i := 0; i < len(rows); i++ {
		rows[i].ShardID = int64(filter.ShardID)
		rows[i].DomainID = filter.DomainID
		rows[i].WorkflowID = filter.WorkflowID
		rows[i].RunID = filter.RunID
	}
	return rows, err
}

// DeleteFromTimerInfoMaps deletes one or more rows from timer_info_maps table
func (mdb *db) DeleteFromTimerInfoMaps(filter *sqldb.TimerInfoMapsFilter) (sql.Result, error) {
	if filter.TimerID != nil {
		return mdb.conn.Exec(deleteKeyInTimerInfoMapSQLQuery, filter.ShardID, filter.DomainID, filter.WorkflowID, filter.RunID, *filter.TimerID)
	}
	return mdb.conn.Exec(deleteTimerInfoMapSQLQuery, filter.ShardID, filter.DomainID, filter.WorkflowID, filter.RunID)
}

var (
	childExecutionInfoColumns = []string{
		"data",
		"data_encoding",
	}
	childExecutionInfoTableName = "child_execution_info_maps"
	childExecutionInfoKey       = "initiated_id"

	deleteChildExecutionInfoMapQry      = makeDeleteMapQry(childExecutionInfoTableName)
	setKeyInChildExecutionInfoMapQry    = makeSetKeyInMapQry(childExecutionInfoTableName, childExecutionInfoColumns, childExecutionInfoKey)
	deleteKeyInChildExecutionInfoMapQry = makeDeleteKeyInMapQry(childExecutionInfoTableName, childExecutionInfoKey)
	getChildExecutionInfoMapQry         = makeGetMapQryTemplate(childExecutionInfoTableName, childExecutionInfoColumns, childExecutionInfoKey)
)

// ReplaceIntoChildExecutionInfoMaps replaces one or more rows in child_execution_info_maps table
func (mdb *db) ReplaceIntoChildExecutionInfoMaps(rows []sqldb.ChildExecutionInfoMapsRow) (sql.Result, error) {
	return mdb.conn.NamedExec(setKeyInChildExecutionInfoMapQry, rows)
}

// SelectFromChildExecutionInfoMaps reads one or more rows from child_execution_info_maps table
func (mdb *db) SelectFromChildExecutionInfoMaps(filter *sqldb.ChildExecutionInfoMapsFilter) ([]sqldb.ChildExecutionInfoMapsRow, error) {
	var rows []sqldb.ChildExecutionInfoMapsRow
	err := mdb.conn.Select(&rows, getChildExecutionInfoMapQry, filter.ShardID, filter.DomainID, filter.WorkflowID, filter.RunID)
	for i := 0; i < len(rows); i++ {
		rows[i].ShardID = int64(filter.ShardID)
		rows[i].DomainID = filter.DomainID
		rows[i].WorkflowID = filter.WorkflowID
		rows[i].RunID = filter.RunID
	}
	return rows, err
}

// DeleteFromChildExecutionInfoMaps deletes one or more rows from child_execution_info_maps table
func (mdb *db) DeleteFromChildExecutionInfoMaps(filter *sqldb.ChildExecutionInfoMapsFilter) (sql.Result, error) {
	if filter.InitiatedID != nil {
		return mdb.conn.Exec(deleteKeyInChildExecutionInfoMapQry, filter.ShardID, filter.DomainID, filter.WorkflowID, filter.RunID, *filter.InitiatedID)
	}
	return mdb.conn.Exec(deleteChildExecutionInfoMapQry, filter.ShardID, filter.DomainID, filter.WorkflowID, filter.RunID)
}

var (
	requestCancelInfoColumns = []string{
		"data",
		"data_encoding",
	}
	requestCancelInfoTableName = "request_cancel_info_maps"
	requestCancelInfoKey       = "initiated_id"

	deleteRequestCancelInfoMapQry      = makeDeleteMapQry(requestCancelInfoTableName)
	setKeyInRequestCancelInfoMapQry    = makeSetKeyInMapQry(requestCancelInfoTableName, requestCancelInfoColumns, requestCancelInfoKey)
	deleteKeyInRequestCancelInfoMapQry = makeDeleteKeyInMapQry(requestCancelInfoTableName, requestCancelInfoKey)
	getRequestCancelInfoMapQry         = makeGetMapQryTemplate(requestCancelInfoTableName, requestCancelInfoColumns, requestCancelInfoKey)
)

// ReplaceIntoRequestCancelInfoMaps replaces one or more rows in request_cancel_info_maps table
func (mdb *db) ReplaceIntoRequestCancelInfoMaps(rows []sqldb.RequestCancelInfoMapsRow) (sql.Result, error) {
	return mdb.conn.NamedExec(setKeyInRequestCancelInfoMapQry, rows)
}

// SelectFromRequestCancelInfoMaps reads one or more rows from request_cancel_info_maps table
func (mdb *db) SelectFromRequestCancelInfoMaps(filter *sqldb.RequestCancelInfoMapsFilter) ([]sqldb.RequestCancelInfoMapsRow, error) {
	var rows []sqldb.RequestCancelInfoMapsRow
	err := mdb.conn.Select(&rows, getRequestCancelInfoMapQry, filter.ShardID, filter.DomainID, filter.WorkflowID, filter.RunID)
	for i := 0; i < len(rows); i++ {
		rows[i].ShardID = int64(filter.ShardID)
		rows[i].DomainID = filter.DomainID
		rows[i].WorkflowID = filter.WorkflowID
		rows[i].RunID = filter.RunID
	}
	return rows, err
}

// DeleteFromRequestCancelInfoMaps deletes one or more rows from request_cancel_info_maps table
func (mdb *db) DeleteFromRequestCancelInfoMaps(filter *sqldb.RequestCancelInfoMapsFilter) (sql.Result, error) {
	if filter.InitiatedID != nil {
		return mdb.conn.Exec(deleteKeyInRequestCancelInfoMapQry, filter.ShardID, filter.DomainID, filter.WorkflowID, filter.RunID, *filter.InitiatedID)
	}
	return mdb.conn.Exec(deleteRequestCancelInfoMapQry, filter.ShardID, filter.DomainID, filter.WorkflowID, filter.RunID)
}

var (
	signalInfoColumns = []string{
		"data",
		"data_encoding",
	}
	signalInfoTableName = "signal_info_maps"
	signalInfoKey       = "initiated_id"

	deleteSignalInfoMapQry      = makeDeleteMapQry(signalInfoTableName)
	setKeyInSignalInfoMapQry    = makeSetKeyInMapQry(signalInfoTableName, signalInfoColumns, signalInfoKey)
	deleteKeyInSignalInfoMapQry = makeDeleteKeyInMapQry(signalInfoTableName, signalInfoKey)
	getSignalInfoMapQry         = makeGetMapQryTemplate(signalInfoTableName, signalInfoColumns, signalInfoKey)
)

// ReplaceIntoSignalInfoMaps replaces one or more rows in signal_info_maps table
func (mdb *db) ReplaceIntoSignalInfoMaps(rows []sqldb.SignalInfoMapsRow) (sql.Result, error) {
	return mdb.conn.NamedExec(setKeyInSignalInfoMapQry, rows)
}

// SelectFromSignalInfoMaps reads one or more rows from signal_info_maps table
func (mdb *db) SelectFromSignalInfoMaps(filter *sqldb.SignalInfoMapsFilter) ([]sqldb.SignalInfoMapsRow, error) {
	var rows []sqldb.SignalInfoMapsRow
	err := mdb.conn.Select(&rows, getSignalInfoMapQry, filter.ShardID, filter.DomainID, filter.WorkflowID, filter.RunID)
	for i := 0; i < len(rows); i++ {
		rows[i].ShardID = int64(filter.ShardID)
		rows[i].DomainID = filter.DomainID
		rows[i].WorkflowID = filter.WorkflowID
		rows[i].RunID = filter.RunID
	}
	return rows, err
}

// DeleteFromSignalInfoMaps deletes one or more rows from signal_info_maps table
func (mdb *db) DeleteFromSignalInfoMaps(filter *sqldb.SignalInfoMapsFilter) (sql.Result, error) {
	if filter.InitiatedID != nil {
		return mdb.conn.Exec(deleteKeyInSignalInfoMapQry, filter.ShardID, filter.DomainID, filter.WorkflowID, filter.RunID, *filter.InitiatedID)
	}
	return mdb.conn.Exec(deleteSignalInfoMapQry, filter.ShardID, filter.DomainID, filter.WorkflowID, filter.RunID)
}

// InsertIntoSignalsRequestedSets inserts one or more rows into signals_requested_sets table
func (mdb *db) InsertIntoSignalsRequestedSets(rows []sqldb.SignalsRequestedSetsRow) (sql.Result, error) {
	return mdb.conn.NamedExec(createSignalsRequestedSetQuery, rows)
}

// SelectFromSignalsRequestedSets reads one or more rows from signals_requested_sets table
func (mdb *db) SelectFromSignalsRequestedSets(filter *sqldb.SignalsRequestedSetsFilter) ([]sqldb.SignalsRequestedSetsRow, error) {
	var rows []sqldb.SignalsRequestedSetsRow
	err := mdb.conn.Select(&rows, getSignalsRequestedSetQuery, filter.ShardID, filter.DomainID, filter.WorkflowID, filter.RunID)
	for i := 0; i < len(rows); i++ {
		rows[i].ShardID = int64(filter.ShardID)
		rows[i].DomainID = filter.DomainID
		rows[i].WorkflowID = filter.WorkflowID
		rows[i].RunID = filter.RunID
	}
	return rows, err
}

// DeleteFromSignalsRequestedSets deletes one or more rows from signals_requested_sets table
func (mdb *db) DeleteFromSignalsRequestedSets(filter *sqldb.SignalsRequestedSetsFilter) (sql.Result, error) {
	if filter.SignalID != nil {
		return mdb.conn.Exec(deleteSignalsRequestedSetQuery, filter.ShardID, filter.DomainID, filter.WorkflowID, filter.RunID, *filter.SignalID)
	}
	return mdb.conn.Exec(deleteAllSignalsRequestedSetQuery, filter.ShardID, filter.DomainID, filter.WorkflowID, filter.RunID)
}

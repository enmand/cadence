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
	"fmt"
	"time"
)

const (
	readSchemaVersionQuery = `SELECT curr_version from schema_version where db_name=$1`

	writeSchemaVersionQuery = `INSERT into schema_version(db_name, creation_time, curr_version, min_compatible_version) VALUES ($1,$2,$3,$4)
										ON CONFLICT (db_name) DO UPDATE 
										  SET creation_time = excluded.creation_time,
										   	  curr_version = excluded.curr_version,
										      min_compatible_version = excluded.min_compatible_version;`

	writeSchemaUpdateHistoryQuery = `INSERT into schema_update_history(year, month, update_time, old_version, new_version, manifest_md5, description) VALUES($1,$2,$3,$4,$5,$6,$7)`

	createSchemaVersionTableQuery = `CREATE TABLE schema_version(db_name VARCHAR(255) not null PRIMARY KEY, ` +
		`creation_time TIMESTAMP, ` +
		`curr_version VARCHAR(64), ` +
		`min_compatible_version VARCHAR(64));`

	createSchemaUpdateHistoryTableQuery = `CREATE TABLE schema_update_history(` +
		`year int not null, ` +
		`month int not null, ` +
		`update_time TIMESTAMP not null, ` +
		`description VARCHAR(255), ` +
		`manifest_md5 VARCHAR(64), ` +
		`new_version VARCHAR(64), ` +
		`old_version VARCHAR(64), ` +
		`PRIMARY KEY (year, month, update_time));`

	//NOTE we have to use %v because somehow mysql doesn't work with ? here
	createDatabaseQuery = "CREATE database %v"

	dropDatabaseQuery = "Drop database %v"

	listTablesQuery = "select table_name from information_schema.tables where table_schema='public'"

	dropTableQuery = "DROP TABLE %v"
)

// CreateSchemaVersionTables sets up the schema version tables
func (mdb *db) CreateSchemaVersionTables() error {
	if err := mdb.Exec(createSchemaVersionTableQuery); err != nil {
		return err
	}
	return mdb.Exec(createSchemaUpdateHistoryTableQuery)
}

// ReadSchemaVersion returns the current schema version for the keyspace
func (mdb *db)  ReadSchemaVersion(database string) (string, error) {
	var version string
	err := mdb.db.Get(&version, readSchemaVersionQuery, database)
	return version, err
}

// UpdateSchemaVersion updates the schema version for the keyspace
func (mdb *db)  UpdateSchemaVersion(database string, newVersion string, minCompatibleVersion string) error {
	return mdb.Exec(writeSchemaVersionQuery, database, time.Now(), newVersion, minCompatibleVersion)
}

// WriteSchemaUpdateLog adds an entry to the schema update history table
func (mdb *db)  WriteSchemaUpdateLog(oldVersion string, newVersion string, manifestMD5 string, desc string) error {
	now := time.Now().UTC()
	return mdb.Exec(writeSchemaUpdateHistoryQuery, now.Year(), int(now.Month()), now, oldVersion, newVersion, manifestMD5, desc)
}

// Exec executes a sql statement
func (mdb *db) Exec(stmt string, args ...interface{}) error {
	_, err := mdb.db.Exec(stmt, args...)
	return err
}

// ListTables returns a list of tables in this database
func (mdb *db)  ListTables(database string) ([]string, error) {
	var tables []string
	err := mdb.db.Select(&tables, fmt.Sprintf(listTablesQuery))
	return tables, err
}

// DropTable drops a given table from the database
func (mdb *db)  DropTable(name string) error {
	return mdb.Exec(fmt.Sprintf(dropTableQuery, name))
}

// DropAllTables drops all tables from this database
func (mdb *db)  DropAllTables(database string) error {
	tables, err := mdb.ListTables(database)
	if err != nil {
		return err
	}
	for _, tab := range tables {
		if err := mdb.DropTable(tab); err != nil {
			return err
		}
	}
	return nil
}

// CreateDatabase creates a database if it doesn't exist
func (mdb *db)  CreateDatabase(name string) error {
	return mdb.Exec(fmt.Sprintf(createDatabaseQuery, name))
}

// DropDatabase drops a database
func (mdb *db)  DropDatabase(name string) error {
	return mdb.Exec(fmt.Sprintf(dropDatabaseQuery, name))
}

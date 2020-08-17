// Package fastsql is a library which extends Go's standard database/sql library.  It provides performance that's easy to take advantage of.
//
// Even better, the fastsql.DB object embeds the standard sql.DB object meaning access to all the standard database/sql library functionality is preserved.  It also means that integrating fastsql into existing codebases is a breeze.
//
// Additional functionality inclues:
//
// 1. Easy, readable, and performant batch insert queries using the BatchInsert method.
// 2. Automatic creation and re-use of prepared statements.
// 3. A convenient holder for manually used prepared statements.
package fastsql

import (
	"database/sql"
	"regexp"
	"strings"
	"sync"
)

var (
	dupeRegexp   = regexp.MustCompile(`(?i)on duplicate key update`)
	valuesRegexp = regexp.MustCompile(`(?i)values`)
)

// DB is a database handle that embeds the standard library's sql.DB struct.
//
//This means the fastsql.DB struct has, and allows, access to all of the standard library functionality while also providng a superset of functionality such as batch operations, autmatically created prepared statmeents, and more.
type DB struct {
	*sql.DB
	PreparedStatements map[string]*sql.Stmt
	prepstmts          map[string]*sql.Stmt
	driverName         string
	flushInterval      uint
	batchInserts       map[string]*insert
}

// Close is the same a sql.Close, but first closes any opened prepared statements.
func (d *DB) Close() error {
	var (
		wg sync.WaitGroup
	)

	if err := d.FlushAll(); err != nil {
		return err
	}

	wg.Add(1)
	go func(wg *sync.WaitGroup) {
		defer wg.Done()

		for _, stmt := range d.PreparedStatements {
			_ = stmt.Close()
		}
	}(&wg)

	wg.Add(1)
	go func(wg *sync.WaitGroup) {
		defer wg.Done()

		for _, stmt := range d.prepstmts {
			_ = stmt.Close()
		}
	}(&wg)

	wg.Wait()
	return d.DB.Close()
}

// Open is the same as sql.Open, but returns an *fastsql.DB instead.
func Open(driverName, dataSourceName string, flushInterval uint) (*DB, error) {
	var (
		err error
		dbh *sql.DB
	)

	if dbh, err = sql.Open(driverName, dataSourceName); err != nil {
		return nil, err
	}

	return &DB{
		DB:                 dbh,
		PreparedStatements: make(map[string]*sql.Stmt),
		prepstmts:          make(map[string]*sql.Stmt),
		driverName:         driverName,
		flushInterval:      flushInterval,
		batchInserts:       make(map[string]*insert),
	}, err
}

// BatchInsert takes a singlular INSERT query and converts it to a batch-insert query for the caller.  A batch-insert is ran every time BatchInsert is called a multiple of flushInterval times.
func (d *DB) BatchInsert(query string, params ...interface{}) (err error) {
	if _, ok := d.batchInserts[query]; !ok {
		d.batchInserts[query] = newInsert()
	} //if

	// Only split out query the first time Insert is called
	if d.batchInserts[query].queryPart1 == "" {
		d.batchInserts[query].splitQuery(query)
	}

	d.batchInserts[query].insertCtr++

	// Build VALUES seciton of query and add to parameter slice
	d.batchInserts[query].values += d.batchInserts[query].queryPart2
	d.batchInserts[query].bindParams = append(d.batchInserts[query].bindParams, params...)

	// If the batch interval has been hit, execute a batch insert
	if d.batchInserts[query].insertCtr >= d.flushInterval {
		err = d.flushInsert(d.batchInserts[query])
	}

	return err
}

// FlushAll iterates over all batch inserts and inserts them into the database.
func (d *DB) FlushAll() error {
	for _, in := range d.batchInserts {
		if err := d.flushInsert(in); err != nil {
			// empty batchInserts
			d.batchInserts = map[string]*insert{}
			return err
		}
	}

	return nil
}

// flushInsert performs the acutal batch-insert query.
func (d *DB) flushInsert(in *insert) error {
	var (
		err   error
		query = in.queryPart1 + in.values[:len(in.values)-1] + in.queryPart3
	)

	// Prepare query
	if _, ok := d.prepstmts[query]; !ok {
		var stmt *sql.Stmt

		if stmt, err = d.DB.Prepare(query); err == nil {
			d.prepstmts[query] = stmt
		} else {
			return err
		}
	}

	// Executate batch insert
	if _, err = d.prepstmts[query].Exec(in.bindParams...); err != nil {
		return err
	} //if

	// Reset vars
	in.values = " VALUES"
	in.bindParams = make([]interface{}, 0)
	in.insertCtr = 0

	return err
}

func (d *DB) setDB(dbh *sql.DB) (err error) {
	if err = dbh.Ping(); err != nil {
		return err
	}

	d.DB = dbh
	return nil
}

type insert struct {
	bindParams []interface{}
	insertCtr  uint
	queryPart1 string
	queryPart2 string
	queryPart3 string
	values     string
}

func newInsert() *insert {
	return &insert{
		bindParams: make([]interface{}, 0),
		values:     " VALUES",
	}
}

func (in *insert) splitQuery(query string) {
	var (
		ndxOnDupe, ndxValues = -1, -1
		ndxParens            = strings.LastIndex(query, ")")
	)

	// Find "VALUES".
	valuesMatches := valuesRegexp.FindStringIndex(query)
	if len(valuesMatches) > 0 {
		ndxValues = valuesMatches[0]
	}

	// Find "ON DUPLICATE KEY UPDATE"
	dupeMatches := dupeRegexp.FindAllStringIndex(query, -1)
	if len(dupeMatches) > 0 {
		ndxOnDupe = dupeMatches[len(dupeMatches)-1][0]
	}

	// Split out first part of query
	in.queryPart1 = strings.TrimSpace(query[:ndxValues])

	// If ON DUPLICATE clause exists, separate into 3 parts.
	// If ON DUPLICATE does not exist, seperate into 2 parts.
	if ndxOnDupe != -1 {
		in.queryPart2 = query[ndxValues+6:ndxOnDupe-1] + ","
		in.queryPart3 = query[ndxOnDupe:]
	} else {
		in.queryPart2 = query[ndxValues+6:ndxParens+1] + ","
	}
}

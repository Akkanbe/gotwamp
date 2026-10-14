//declscope:namespace sqlite

package sqlite

import "database/sql"

// SetSchemaVersion rewrites the schema version of the database at path, to
// test a database written by another gotwampd.
func SetSchemaVersion(path string, v int) error {
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`UPDATE schema_version SET version = ?`, v)
	return err
}

// ExecSQL runs q on the database at path, to make a database as another
// gotwampd would have left it.
func ExecSQL(path, q string, args ...any) error {
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(q, args...)
	return err
}

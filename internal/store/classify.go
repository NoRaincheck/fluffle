package store

import (
	"errors"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func classify(err error) error {
	if err == nil {
		return nil
	}
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return err
	}
	switch se.Code() {
	case sqlite3.SQLITE_CONSTRAINT_UNIQUE:
		return ErrConflict
	case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
		return ErrNotFound
	case sqlite3.SQLITE_CONSTRAINT_CHECK, sqlite3.SQLITE_CONSTRAINT_NOTNULL:
		return ErrInvalid
	}
	return err
}

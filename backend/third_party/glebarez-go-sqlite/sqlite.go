// Package sqlite adapts the error/driver types used by the GORM dialect to
// Sub2API's existing modernc SQLite driver. Importing modernc registers the
// "sqlite" driver exactly once, including builds that use Ent SQLite tests.
package sqlite

import modernc "modernc.org/sqlite"

type Error = modernc.Error
type Driver = modernc.Driver

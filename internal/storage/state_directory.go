package storage

import "path/filepath"

// StateDirectory is empty for in-memory databases. Auxiliary Server state lives
// beside the actual database, including when Open was given a SQLite URI.
func (s *Store) StateDirectory() string {
	if s.databasePath == "" {
		return ""
	}
	return filepath.Dir(s.databasePath)
}

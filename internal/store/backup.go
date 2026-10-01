package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BackupTo writes a consistent snapshot of the whole database to dst.
//
// This exists because the obvious thing — copying qingzhou.db — is wrong here.
// The database runs in WAL mode, so at any moment an arbitrary amount of
// committed data lives in the -wal file and not in the main file at all. A copy
// of qingzhou.db alone is a torn, stale database; a copy of all three files
// taken while a write is in flight is worse, because it looks intact and only
// fails later. Restoring either one loses orders, traffic counters, or the
// certificate table, and the operator finds out at the moment they can least
// afford to.
//
// VACUUM INTO takes the snapshot from inside SQLite: it runs against a read
// transaction, so it sees exactly one committed point in time, includes
// everything in the WAL, and produces a single self-contained file with no
// sidecars. Readers and writers keep working while it runs.
//
// dst must not already exist. The completed, fsynced 0600 snapshot is published
// atomically without replacing any existing path, even under concurrent calls.
// A failed snapshot leaves no partial destination or staging directory.
func (s *Store) BackupTo(dst string) error {
	if strings.TrimSpace(dst) == "" {
		return fmt.Errorf("backup destination is empty")
	}
	abs, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(abs); err == nil {
		return fmt.Errorf("backup destination already exists: %s", abs)
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(abs)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	// SQLite creates the file itself. Keep it inside a private same-filesystem
	// directory until it has its final mode and has been synced, so even a
	// permissive process umask never exposes an unfinished database.
	dir, err := os.MkdirTemp(parent, ".qingzhou-vacuum-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	tmp := filepath.Join(dir, "snapshot.db")
	// Binding the path also handles directory names containing SQL quotes.
	if _, err := s.db.Exec(`VACUUM INTO ?`, tmp); err != nil {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// Link publishes complete bytes atomically AND refuses an existing name.
	// Rename would clobber another concurrent backup (or a symlink) after the
	// initial existence check. The staging file is on this same filesystem.
	if err := os.Link(tmp, abs); err != nil {
		return err
	}
	d, err := os.Open(parent)
	if err != nil {
		os.Remove(abs)
		return err
	}
	err = d.Sync()
	closeErr := d.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(abs)
		return err
	}
	return nil
}

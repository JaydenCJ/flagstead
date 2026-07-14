// Store: the file-backed snapshot the HTTP handlers read from.
//
// flagstead has no database and no admin API — the TOML file on disk is
// the single source of truth. The store stats the file on each request
// (a stat is a few microseconds; no watcher threads, no inotify
// portability problems) and reparses only when mtime or size changed.
// A broken edit NEVER takes flags down: the last good snapshot keeps
// serving and the error is surfaced on /healthz until the file is fixed.
package server

import (
	"os"
	"sync"
	"time"

	"github.com/JaydenCJ/flagstead/internal/flagfile"
)

// Store hands out the current parsed flag file.
type Store struct {
	path string

	mu      sync.Mutex
	file    *flagfile.File
	modTime time.Time
	size    int64
	loadErr error // sticky until a reload succeeds
}

// NewStore loads path once, eagerly, so `flagstead serve` refuses to
// start on a file that has never been valid.
func NewStore(path string) (*Store, error) {
	s := &Store{path: path}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if err := s.load(info); err != nil {
		return nil, err
	}
	return s, nil
}

// Snapshot returns the current file, reloading first when the file on
// disk changed. The returned file is never nil; the error (when non-nil)
// means the file on disk is currently broken and the snapshot is the
// last good version.
func (s *Store) Snapshot() (*flagfile.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	info, err := os.Stat(s.path)
	if err != nil {
		s.loadErr = err
		return s.file, s.loadErr
	}
	if !info.ModTime().Equal(s.modTime) || info.Size() != s.size {
		if err := s.load(info); err != nil {
			// Remember the stamp anyway so a broken file is not reparsed
			// on every single request; a fix touches mtime again.
			s.modTime = info.ModTime()
			s.size = info.Size()
			s.loadErr = err
		}
	}
	return s.file, s.loadErr
}

func (s *Store) load(info os.FileInfo) error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	f, err := flagfile.Parse(raw)
	if err != nil {
		return err
	}
	s.file = f
	s.modTime = info.ModTime()
	s.size = info.Size()
	s.loadErr = nil
	return nil
}

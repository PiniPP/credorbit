// Package fsutil provides crash-safe file replacement, timestamped backups,
// advisory locking and restrictive permissions for sensitive files.
package fsutil

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// AtomicWrite writes data to path by creating a temp file in the SAME directory
// (so the final rename never crosses filesystems), fsyncing it, applying perm and
// renaming it over the target. On any error the original file is untouched.
func AtomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(perm); err != nil && !isUnsupported(err) {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := replaceFile(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	ok = true
	if err := Restrict(path, perm); err != nil {
		return err
	}
	syncDir(dir)
	return nil
}

func isUnsupported(err error) bool {
	return errors.Is(err, errors.ErrUnsupported)
}

// Backup copies path to "<path>.credorbit-<UTC timestamp>.bak" next to it and
// prunes older CredOrbit backups beyond keep. Missing source is not an error and
// returns "". The backup gets the same restrictive permissions as the source.
func Backup(path string, keep int) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s for backup: %w", path, err)
	}
	perm := os.FileMode(0o600)
	dst := fmt.Sprintf("%s.credorbit-%s.bak", path, time.Now().UTC().Format("20060102T150405.000Z"))
	if err := AtomicWrite(dst, data, perm); err != nil {
		return "", fmt.Errorf("write backup: %w", err)
	}
	if keep > 0 {
		pruneBackups(path, keep)
	}
	return dst, nil
}

// ListBackups returns CredOrbit backups of path, newest first.
func ListBackups(path string) []string {
	matches, _ := filepath.Glob(path + ".credorbit-*.bak")
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	return matches
}

func pruneBackups(path string, keep int) {
	for i, b := range ListBackups(path) {
		if i >= keep {
			_ = os.Remove(b)
		}
	}
}

// WriteIfChanged atomically writes data only if it differs from the current
// content. It returns whether a write happened.
func WriteIfChanged(path string, data []byte, perm os.FileMode) (bool, error) {
	cur, err := os.ReadFile(path)
	if err == nil && bytes.Equal(cur, data) {
		return false, nil
	}
	return true, AtomicWrite(path, data, perm)
}

// ReadFileOptional returns nil, nil when the file does not exist.
func ReadFileOptional(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// ExpandHome expands a leading "~" or "~/" to the user's home directory.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[1:])
		}
	}
	return p
}

// Lock is an advisory, cross-process lock on "<path>.lock".
type Lock struct {
	f    *os.File
	path string
}

// ErrLocked is returned by TryLock when another process holds the lock.
var ErrLocked = errors.New("file is locked by another CredOrbit process")

// AcquireLock waits up to timeout for an exclusive lock on target+".lock".
func AcquireLock(target string, timeout time.Duration) (*Lock, error) {
	lp := target + ".lock"
	if err := os.MkdirAll(filepath.Dir(lp), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(lp, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for {
		err := tryLock(f)
		if err == nil {
			return &Lock{f: f, path: lp}, nil
		}
		if !errors.Is(err, ErrLocked) || time.Now().After(deadline) {
			_ = f.Close()
			if errors.Is(err, ErrLocked) {
				return nil, fmt.Errorf("%s: %w", target, ErrLocked)
			}
			return nil, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Release unlocks. Safe to call on nil.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = unlock(l.f)
	_ = l.f.Close()
	l.f = nil
}

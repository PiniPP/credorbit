//go:build !windows

package fsutil

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func replaceFile(src, dst string) error { return os.Rename(src, dst) }

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// Restrict applies perm (typically 0600) to path.
func Restrict(path string, perm os.FileMode) error {
	if err := os.Chmod(path, perm); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

// CheckPrivate reports a problem if path is readable or writable by group/others.
func CheckPrivate(path string) (ok bool, detail string, err error) {
	st, err := os.Stat(path)
	if err != nil {
		return false, "", err
	}
	mode := st.Mode().Perm()
	if mode&0o077 != 0 {
		return false, fmt.Sprintf("mode %04o allows group/other access (want 0600)", mode), nil
	}
	return true, fmt.Sprintf("mode %04o", mode), nil
}

func tryLock(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return ErrLocked
	}
	return err
}

func unlock(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_UN) }

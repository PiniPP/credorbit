//go:build windows

package fsutil

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

func replaceFile(src, dst string) error {
	from, err := windows.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func syncDir(string) {}

// Restrict replaces the file's DACL with a protected ACL granting full control to
// the current user only (inheritance disabled). perm is ignored on Windows.
func Restrict(path string, _ os.FileMode) error {
	sid, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("resolve current user SID: %w", err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + sid.String() + ")")
	if err != nil {
		return fmt.Errorf("build security descriptor: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read DACL: %w", err)
	}
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
	if err != nil {
		return fmt.Errorf("set ACL on %s: %w", path, err)
	}
	return nil
}

// CheckPrivate reports whether the DACL grants access to anyone other than the
// current user, SYSTEM or Administrators.
func CheckPrivate(path string) (bool, string, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, "", err
	}
	s := sd.String()
	for _, broad := range []string{";;;WD)", ";;;BU)", ";;;AU)", ";;;IU)"} { // Everyone, Users, Authenticated, Interactive
		if strings.Contains(s, broad) {
			return false, "ACL grants access to a broad group (Everyone/Users)", nil
		}
	}
	return true, "ACL restricted", nil
}

func currentUserSID() (*windows.SID, error) {
	tok := windows.GetCurrentProcessToken()
	u, err := tok.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return u.User.Sid, nil
}

func tryLock(f *os.File) error {
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return ErrLocked
	}
	return err
}

func unlock(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
}

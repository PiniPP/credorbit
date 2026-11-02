package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestAtomicWriteCreatesAndReplaces(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "file")
	if err := AtomicWrite(p, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(p, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "two" {
		t.Fatalf("got %q", b)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(p)
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", st.Mode().Perm())
		}
		ok, _, err := CheckPrivate(p)
		if err != nil || !ok {
			t.Fatalf("CheckPrivate ok=%v err=%v", ok, err)
		}
	}
	// no temp files left behind
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 1 {
		t.Fatalf("leftover files: %v", ents)
	}
}

func TestAtomicWriteFailureKeepsOriginal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission semantics differ")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := AtomicWrite(p, []byte("orig"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil { // dir not writable → temp create fails
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	if err := AtomicWrite(p, []byte("new"), 0o600); err == nil {
		t.Skip("running as root; cannot simulate failure")
	}
	b, _ := os.ReadFile(p)
	if string(b) != "orig" {
		t.Fatalf("original modified: %q", b)
	}
}

func TestCheckPrivateDetectsLoose(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix modes")
	}
	p := filepath.Join(t.TempDir(), "f")
	os.WriteFile(p, []byte("x"), 0o644)
	ok, detail, err := CheckPrivate(p)
	if err != nil || ok || detail == "" {
		t.Fatalf("ok=%v detail=%q err=%v", ok, detail, err)
	}
}

func TestBackupAndPrune(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config")
	if b, err := Backup(p, 3); err != nil || b != "" {
		t.Fatalf("missing source: %q %v", b, err)
	}
	os.WriteFile(p, []byte("data"), 0o600)
	var last string
	for i := 0; i < 5; i++ {
		b, err := Backup(p, 3)
		if err != nil {
			t.Fatal(err)
		}
		last = b
		time.Sleep(2 * time.Millisecond)
	}
	bs := ListBackups(p)
	if len(bs) != 3 {
		t.Fatalf("want 3 backups, got %d", len(bs))
	}
	if bs[0] != last {
		t.Fatalf("newest first: %v vs %s", bs, last)
	}
	c, _ := os.ReadFile(last)
	if string(c) != "data" {
		t.Fatal("backup content")
	}
}

func TestWriteIfChanged(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	w, err := WriteIfChanged(p, []byte("a"), 0o600)
	if err != nil || !w {
		t.Fatal("first write")
	}
	w, err = WriteIfChanged(p, []byte("a"), 0o600)
	if err != nil || w {
		t.Fatal("unchanged should not write")
	}
}

func TestLockExclusive(t *testing.T) {
	p := filepath.Join(t.TempDir(), "creds")
	l1, err := AcquireLock(p, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// flock is per open-file-description, so a second open in-process conflicts.
	_, err = AcquireLock(p, 200*time.Millisecond)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("want ErrLocked, got %v", err)
	}
	l1.Release()
	l2, err := AcquireLock(p, time.Second)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	l2.Release()
	l2.Release() // idempotent
	var nilLock *Lock
	nilLock.Release()
}

func TestLockSerializesWriters(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := AcquireLock(p, 10*time.Second)
			if err != nil {
				t.Error(err)
				return
			}
			defer l.Release()
			b, _ := ReadFileOptional(p)
			_ = AtomicWrite(p, append(b, 'x'), 0o600)
		}()
	}
	wg.Wait()
	b, _ := os.ReadFile(p)
	if len(b) != 8 {
		t.Fatalf("lost update: %d", len(b))
	}
}

func TestExpandHome(t *testing.T) {
	h, _ := os.UserHomeDir()
	if got := ExpandHome("~/x"); got != filepath.Join(h, "x") {
		t.Fatal(got)
	}
	if got := ExpandHome("/abs"); got != "/abs" {
		t.Fatal(got)
	}
}

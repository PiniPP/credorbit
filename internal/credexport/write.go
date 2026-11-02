package credexport

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/credorbit/credorbit/internal/awsini"
	"github.com/credorbit/credorbit/internal/fsutil"
	"github.com/credorbit/credorbit/internal/model"
)

// ExpirationKey is written into every exported section (the AWS CLI and SDKs
// ignore unknown keys).
const ExpirationKey = "x_credorbit_expiration"

// DefaultBackupKeep is used when WriteOptions.BackupKeep <= 0.
const DefaultBackupKeep = 5

// WriteOptions controls Write.
type WriteOptions struct {
	DefaultFrom string   // section (or profile name) copied into [default]; "" = leave [default] alone
	Prune       []string // managed sections to delete (profiles removed); only these are ever deleted
	BackupKeep  int      // default 5
}

// WriteReport describes what Write did.
type WriteReport struct {
	Written  []string // sections replaced or added, in result order ("default" last if refreshed)
	Kept     []string // failed targets whose existing section was left untouched
	Pruned   []string // sections deleted because they were listed in Prune
	Backup   string   // backup of the previous file ("" if there was none)
	Warnings []string // non-fatal issues, e.g. [default] could not be refreshed
}

// ErrNothingToWrite is returned when no credentials were fetched; the file is
// left untouched.
var ErrNothingToWrite = errors.New("no credentials were fetched")

// Write updates the credentials file at path under a lock: only sections of
// succeeded items are replaced (keys aws_access_key_id, aws_secret_access_key,
// aws_session_token, x_credorbit_expiration; other keys in those sections are
// kept); failed targets keep their old section; sections listed in o.Prune are
// removed; everything else is preserved byte-for-byte. [default] receives a
// copy of DefaultFrom's credentials only if DefaultFrom succeeded in r;
// otherwise [default] is left alone and a warning is reported. Zero successes
// → ErrNothingToWrite, file untouched. The old file is backed up, the new one
// written atomically with mode 0600.
//
// Deviation from the original contract (written, backup, err): a *WriteReport
// is returned so callers also learn about kept/pruned sections and warnings.
func Write(path string, r *Result, o WriteOptions) (*WriteReport, error) {
	rep := &WriteReport{}
	if r == nil || r.Succeeded() == 0 {
		return rep, fmt.Errorf("%w; %s left unchanged", ErrNothingToWrite, path)
	}
	if o.BackupKeep <= 0 {
		o.BackupKeep = DefaultBackupKeep
	}
	lock, err := fsutil.AcquireLock(path, 10*time.Second)
	if err != nil {
		return rep, err
	}
	defer lock.Release()

	orig, err := fsutil.ReadFileOptional(path)
	if err != nil {
		return rep, fmt.Errorf("read %s: %w", path, err)
	}
	f, err := awsini.Parse(orig)
	if err != nil {
		return rep, fmt.Errorf("parse %s: %w", path, err)
	}

	written := map[string]model.Credentials{}
	var order []string
	for _, it := range r.Items {
		sec := sectionOf(it)
		if !it.OK() {
			continue
		}
		if _, dup := written[sec]; !dup {
			order = append(order, sec)
		}
		written[sec] = it.Credentials()
	}
	for _, it := range r.Items {
		sec := sectionOf(it)
		if _, ok := written[sec]; !ok && !it.OK() && f.Section(sec) != nil && !contains(rep.Kept, sec) {
			rep.Kept = append(rep.Kept, sec)
		}
	}
	for _, sec := range order {
		putCreds(f, sec, written[sec])
	}
	rep.Written = append(rep.Written, order...)

	if o.DefaultFrom != "" {
		if c, ok := defaultSource(r, o.DefaultFrom); ok {
			putCreds(f, "default", c)
			if !contains(rep.Written, "default") {
				rep.Written = append(rep.Written, "default")
			}
		} else if it, found := findItem(r, o.DefaultFrom); found {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("[default] not refreshed: %s failed (%v); existing [default] left unchanged", o.DefaultFrom, it.Err))
		} else {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("[default] not refreshed: %s was not exported in this run; existing [default] left unchanged", o.DefaultFrom))
		}
	}

	for _, name := range o.Prune {
		if name == "" || name == "default" {
			continue
		}
		if _, ok := written[name]; ok {
			continue
		}
		removed := false
		for f.RemoveSection(name) {
			removed = true
		}
		if removed {
			rep.Pruned = append(rep.Pruned, name)
		}
	}

	out := f.Bytes()
	if err := validateCreds(out, rep.Written, written); err != nil {
		return rep, fmt.Errorf("refusing to write %s: %w", path, err)
	}
	if rep.Backup, err = fsutil.Backup(path, o.BackupKeep); err != nil {
		return rep, err
	}
	if err := fsutil.AtomicWrite(path, out, 0o600); err != nil {
		return rep, err
	}
	if err := fsutil.Restrict(path, 0o600); err != nil {
		return rep, err
	}
	return rep, nil
}

func sectionOf(it Item) string {
	if it.Target.Section != "" {
		return it.Target.Section
	}
	return it.Target.Profile.Name
}

func defaultSource(r *Result, from string) (model.Credentials, bool) {
	if it, ok := findItem(r, from); ok && it.OK() {
		return it.Credentials(), true
	}
	return model.Credentials{}, false
}

// findItem matches by section name first, then by profile name. A successful
// item is preferred over a failed one with the same name.
func findItem(r *Result, name string) (Item, bool) {
	var found *Item
	for i := range r.Items {
		it := &r.Items[i]
		if sectionOf(*it) == name || it.Target.Profile.Name == name {
			if it.OK() {
				return *it, true
			}
			if found == nil {
				found = it
			}
		}
	}
	if found != nil {
		return *found, true
	}
	return Item{}, false
}

// putCreds replaces the credential keys of section name (creating it if
// needed) and removes later duplicate sections of the same name, which would
// otherwise make the AWS CLI reject the file.
func putCreds(f *awsini.File, name string, c model.Credentials) {
	dups := f.SectionsNamed(name)
	for _, d := range dups[min(1, len(dups)):] {
		f.Remove(d)
	}
	s := f.Section(name)
	if s == nil {
		s = f.AddSection(name)
	}
	s.Set("aws_access_key_id", c.AccessKeyID)
	s.Set("aws_secret_access_key", c.SecretAccessKey)
	s.Set("aws_session_token", c.SessionToken)
	if c.Expiration.IsZero() {
		s.Delete(ExpirationKey)
	} else {
		s.Set(ExpirationKey, c.Expiration.UTC().Format(time.RFC3339))
	}
}

func validateCreds(out []byte, sections []string, creds map[string]model.Credentials) error {
	g, err := awsini.Parse(out)
	if err != nil {
		return err
	}
	for _, sec := range sections {
		s := g.Section(sec)
		if s == nil {
			return fmt.Errorf("validation: section [%s] missing", sec)
		}
		id, _ := s.Get("aws_access_key_id")
		if c, ok := creds[sec]; ok && id != c.AccessKeyID {
			return fmt.Errorf("validation: section [%s] has unexpected key id", sec)
		}
		if id == "" {
			return fmt.Errorf("validation: section [%s] has no access key", sec)
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// SectionName returns the credentials section for a profile: with stripPrefix
// and a matching prefix the prefix is removed ("p_x" → "x", legacy compat),
// otherwise the profile name itself.
func SectionName(profile, prefix string, stripPrefix bool) string {
	if stripPrefix && prefix != "" && strings.HasPrefix(profile, prefix) && len(profile) > len(prefix) {
		return strings.TrimPrefix(profile, prefix)
	}
	return profile
}

// ReadExpirations returns section → expiration for every section of the
// credentials file that has a valid x_credorbit_expiration key. A missing or
// unreadable file yields an empty map.
func ReadExpirations(path string) map[string]time.Time {
	m := map[string]time.Time{}
	data, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	f, err := awsini.Parse(data)
	if err != nil {
		return m
	}
	for _, s := range f.Sections() {
		if _, seen := m[s.Name()]; seen {
			continue
		}
		if v, ok := s.Get(ExpirationKey); ok {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				m[s.Name()] = t
			}
		}
	}
	return m
}

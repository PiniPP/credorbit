package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/credorbit/credorbit/internal/fsutil"
	"github.com/credorbit/credorbit/internal/model"
)

// CurrentStateVersion is the state.json schema version written by this build.
const CurrentStateVersion = 1

// MaxRotationHistory caps State.Rotation.History.
const MaxRotationHistory = 50

// ErrCorruptState is wrapped by LoadState when state.json cannot be parsed.
var ErrCorruptState = errors.New("state file is corrupt")

// State is machine-managed data in state.json. It never contains secrets.
type State struct {
	Version         int                         `json:"version"`
	Discoveries     map[string]*model.Discovery `json:"discoveries"` // by session name
	ManagedSections []string                    `json:"managed_sections"`
	LastRole        map[string]string           `json:"last_role"` // profile → role last used successfully
	LastRun         time.Time                   `json:"last_run"`
	Rotation        RotationState               `json:"rotation"`
	// LegacyImported records when the legacy script was imported (doctor hint).
	LegacyImported time.Time `json:"legacy_imported,omitzero"`
}

// RotationState records the current rotation target and recent changes.
type RotationState struct {
	Current string          `json:"current"`
	History []RotationEvent `json:"history"`
}

// RotationEvent is one rotation from one account (section) to another.
type RotationEvent struct {
	At   time.Time `json:"at"`
	From string    `json:"from"`
	To   string    `json:"to"`
}

// NewState returns an empty, initialized state.
func NewState() *State {
	s := &State{Version: CurrentStateVersion}
	s.normalize()
	return s
}

func (s *State) normalize() {
	if s.Discoveries == nil {
		s.Discoveries = map[string]*model.Discovery{}
	}
	for k, d := range s.Discoveries {
		if d == nil {
			delete(s.Discoveries, k)
		}
	}
	if s.LastRole == nil {
		s.LastRole = map[string]string{}
	}
	s.ManagedSections = sortedUnique(s.ManagedSections)
	if s.Rotation.History == nil {
		s.Rotation.History = []RotationEvent{}
	}
	if n := len(s.Rotation.History); n > MaxRotationHistory {
		s.Rotation.History = append([]RotationEvent(nil), s.Rotation.History[n-MaxRotationHistory:]...)
	}
}

// LoadState reads state.json. A missing file yields an empty state. Unparsable
// content returns an error wrapping ErrCorruptState. Every cached discovery is
// marked Cached=true.
func LoadState(path string) (*State, error) {
	data, err := fsutil.ReadFileOptional(path)
	if err != nil {
		return nil, fmt.Errorf("read state %s: %w", path, err)
	}
	if data == nil {
		return NewState(), nil
	}
	s := &State{}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrCorruptState, path, err)
	}
	if s.Version > CurrentStateVersion {
		return nil, fmt.Errorf("state %s written by newer CredOrbit (version %d, this build supports %d)", path, s.Version, CurrentStateVersion)
	}
	s.Version = CurrentStateVersion
	s.normalize()
	for _, d := range s.Discoveries {
		d.Cached = true
	}
	return s, nil
}

// LoadStateOrReset is LoadState that never fails: a corrupt (or newer) file
// is renamed to "<path>.corrupt-<UTC ts>" and a fresh state is returned with a
// human-readable warning. warning is "" when the file loaded cleanly.
func LoadStateOrReset(path string) (*State, string) {
	s, err := LoadState(path)
	if err == nil {
		return s, ""
	}
	if _, statErr := os.Stat(path); statErr != nil {
		return NewState(), fmt.Sprintf("state unreadable, starting fresh: %v", err)
	}
	dst := fmt.Sprintf("%s.corrupt-%s", path, time.Now().UTC().Format("20060102T150405Z"))
	if rerr := os.Rename(path, dst); rerr != nil {
		return NewState(), fmt.Sprintf("%v; could not move it aside (%v), starting fresh", err, rerr)
	}
	return NewState(), fmt.Sprintf("%v; moved to %s, starting fresh", err, dst)
}

// Save atomically writes state.json with 0600 permissions.
func (s *State) Save(path string) error {
	s.Version = CurrentStateVersion
	s.normalize()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')
	if err := fsutil.AtomicWrite(path, data, 0o600); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}

// AddManaged records sections CredOrbit wrote to the credentials file.
func (s *State) AddManaged(sections ...string) {
	s.ManagedSections = sortedUnique(append(s.ManagedSections, sections...))
}

// RemoveManaged forgets sections; it reports whether any were present.
func (s *State) RemoveManaged(sections ...string) bool {
	drop := make(map[string]bool, len(sections))
	for _, x := range sections {
		drop[x] = true
	}
	kept := s.ManagedSections[:0]
	removed := false
	for _, x := range s.ManagedSections {
		if drop[x] {
			removed = true
			continue
		}
		kept = append(kept, x)
	}
	s.ManagedSections = kept
	return removed
}

// IsManaged reports whether CredOrbit wrote section.
func (s *State) IsManaged(section string) bool {
	i := sort.SearchStrings(s.ManagedSections, section)
	return i < len(s.ManagedSections) && s.ManagedSections[i] == section
}

// SetDiscovery stores a (live) discovery for its session.
func (s *State) SetDiscovery(d *model.Discovery) {
	if d == nil {
		return
	}
	if s.Discoveries == nil {
		s.Discoveries = map[string]*model.Discovery{}
	}
	s.Discoveries[d.Session] = d
}

// RecordRotation sets the current rotation account and appends a history
// entry, keeping at most MaxRotationHistory entries.
func (s *State) RecordRotation(from, to string, at time.Time) {
	s.Rotation.Current = to
	s.Rotation.History = append(s.Rotation.History, RotationEvent{At: at.UTC(), From: from, To: to})
	if n := len(s.Rotation.History); n > MaxRotationHistory {
		s.Rotation.History = append([]RotationEvent(nil), s.Rotation.History[n-MaxRotationHistory:]...)
	}
}

func sortedUnique(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, x := range in {
		if x == "" || seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
	}
	sort.Strings(out)
	return out
}

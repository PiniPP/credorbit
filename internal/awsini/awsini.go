// Package awsini is a round-trip preserving, line-based INI editor for the AWS
// shared config (~/.aws/config) and credentials (~/.aws/credentials) files.
//
// Parse followed by Bytes reproduces the input byte-for-byte: line endings
// (LF, CRLF, mixed), a missing trailing newline, comments (# and ;), blank
// lines, indentation, keys before the first section, spacing around '=',
// duplicate keys and sections, and nested sub-property blocks such as
//
//	s3 =
//	  max_concurrent_requests = 20
//
// (indented continuation lines belong to the key above them). Edits touch only
// the lines they concern; everything else is preserved verbatim.
package awsini

import (
	"strings"
)

type kind uint8

const (
	kBlank   kind = iota // empty or whitespace only
	kComment             // first non-space char is '#' or ';'
	kHeader              // [section]
	kKey                 // key = value
	kCont                // continuation line of the preceding key (indented deeper)
	kOther               // anything else (preserved, otherwise ignored)
)

type line struct {
	raw  string // line content without its line terminator
	eol  string // "\n", "\r\n" or "" (last line without terminator)
	kind kind
	name string // header: normalized section name; key: key name as written
}

// File is a parsed INI file. The zero value is not usable; use Parse.
type File struct {
	lines []*line
	nl    string // dominant newline style, used for inserted lines
}

// Section is a view of one "[name]" block of a File. It stays valid across
// edits of the File; after the section is removed its methods are no-ops.
type Section struct {
	f   *File
	hdr *line
}

// Parse never fails on malformed input: lines it does not understand are kept
// verbatim and ignored. The error return is reserved for future validation.
func Parse(data []byte) (*File, error) {
	f := &File{}
	s := string(data)
	var crlf, lf int
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		var raw, eol string
		if i < 0 {
			raw, s = s, ""
		} else {
			raw, s = s[:i], s[i+1:]
			eol = "\n"
			if strings.HasSuffix(raw, "\r") {
				raw, eol = raw[:len(raw)-1], "\r\n"
				crlf++
			} else {
				lf++
			}
		}
		f.lines = append(f.lines, &line{raw: raw, eol: eol})
	}
	f.nl = "\n"
	if crlf > lf {
		f.nl = "\r\n"
	}
	f.classify()
	return f, nil
}

// classify (re)computes the kind of every line.
func (f *File) classify() {
	keyIndent := -1 // indentation of the key that may own continuation lines
	for i, l := range f.lines {
		raw := l.raw
		if i == 0 {
			raw = strings.TrimPrefix(raw, "\uFEFF")
		}
		t := strings.TrimSpace(raw)
		indent := len(raw) - len(strings.TrimLeft(raw, " \t"))
		l.name = ""
		switch {
		case t == "":
			l.kind = kBlank
			keyIndent = -1
		case keyIndent >= 0 && indent > keyIndent:
			l.kind = kCont
		case t[0] == '#' || t[0] == ';':
			l.kind = kComment
		case t[0] == '[' && strings.IndexByte(t, ']') > 0:
			l.kind = kHeader
			l.name = normalize(t[1:strings.IndexByte(t, ']')])
			keyIndent = -1
		case strings.IndexByte(t, '=') >= 0:
			l.kind = kKey
			l.name = strings.TrimSpace(t[:strings.IndexByte(t, '=')])
			keyIndent = indent
		default:
			l.kind = kOther
			keyIndent = -1
		}
	}
}

// normalize trims and collapses internal whitespace: "[ profile   x ]" → "profile x".
func normalize(name string) string { return strings.Join(strings.Fields(name), " ") }

// Bytes renders the file. Unmodified files are reproduced byte-for-byte.
func (f *File) Bytes() []byte {
	n := 0
	for _, l := range f.lines {
		n += len(l.raw) + len(l.eol)
	}
	b := make([]byte, 0, n)
	for _, l := range f.lines {
		b = append(b, l.raw...)
		b = append(b, l.eol...)
	}
	return b
}

// Newline returns the file's dominant line terminator ("\n" or "\r\n").
func (f *File) Newline() string { return f.nl }

// Section returns the first section whose header matches name (compared after
// trimming and collapsing whitespace inside the brackets), or nil.
func (f *File) Section(name string) *Section {
	name = normalize(name)
	for _, l := range f.lines {
		if l.kind == kHeader && l.name == name {
			return &Section{f: f, hdr: l}
		}
	}
	return nil
}

// Sections returns every section in file order, duplicates included.
func (f *File) Sections() []*Section {
	var out []*Section
	for _, l := range f.lines {
		if l.kind == kHeader {
			out = append(out, &Section{f: f, hdr: l})
		}
	}
	return out
}

// SectionsNamed returns every section with the given name (more than one when
// the file contains duplicate headers).
func (f *File) SectionsNamed(name string) []*Section {
	name = normalize(name)
	var out []*Section
	for _, l := range f.lines {
		if l.kind == kHeader && l.name == name {
			out = append(out, &Section{f: f, hdr: l})
		}
	}
	return out
}

// Duplicates lists section names that appear more than once, in order of
// their first occurrence. Useful for diagnostics: the AWS CLI rejects such files.
func (f *File) Duplicates() []string {
	count := map[string]int{}
	var order []string
	for _, l := range f.lines {
		if l.kind != kHeader {
			continue
		}
		if count[l.name] == 0 {
			order = append(order, l.name)
		}
		count[l.name]++
	}
	var out []string
	for _, n := range order {
		if count[n] > 1 {
			out = append(out, n)
		}
	}
	return out
}

// AddSection appends "[name]" at the end of the file separated from the
// previous content by exactly one blank line, using the file's newline style.
// If a section with that name already exists it is returned unchanged.
func (f *File) AddSection(name string) *Section {
	name = normalize(sanitize(name))
	if s := f.Section(name); s != nil {
		return s
	}
	if n := len(f.lines); n > 0 {
		if f.lines[n-1].eol == "" {
			f.lines[n-1].eol = f.nl
		}
		blanks := 0
		for i := n - 1; i >= 0 && f.lines[i].kind == kBlank; i-- {
			blanks++
		}
		switch {
		case blanks == 0:
			f.lines = append(f.lines, &line{eol: f.nl, kind: kBlank})
		case blanks > 1:
			f.lines = f.lines[:n-blanks+1]
		}
	}
	hdr := &line{raw: "[" + name + "]", eol: f.nl, kind: kHeader, name: name}
	f.lines = append(f.lines, hdr)
	f.classify()
	return &Section{f: f, hdr: hdr}
}

// RemoveSection removes the first section named name. See Remove.
func (f *File) RemoveSection(name string) bool {
	return f.Remove(f.Section(name))
}

// Remove deletes the section's header, its body and the comment block
// immediately above the header (no blank line in between) unless that block
// directly follows a key of the previous section. A comment block directly
// above the NEXT header belongs to the next section and is kept. Trailing blank
// lines left at the end of the file are trimmed.
func (f *File) Remove(s *Section) bool {
	if s == nil || s.f != f {
		return false
	}
	h := f.indexOf(s.hdr)
	if h < 0 {
		return false
	}
	start := f.leadingStart(h)
	next := f.nextHeader(h + 1)
	end := next
	if next < len(f.lines) {
		end = f.leadingStart(next)
		if end <= h {
			end = next
		}
	}
	throughEOF := end == len(f.lines)
	f.lines = append(f.lines[:start], f.lines[end:]...)
	if throughEOF {
		n := len(f.lines)
		for n > 0 && f.lines[n-1].kind == kBlank {
			n--
		}
		f.lines = f.lines[:n]
	}
	f.classify()
	return true
}

// leadingStart returns the index where the comment block attached to the
// header at h begins (h itself if there is none).
func (f *File) leadingStart(h int) int {
	j := h
	for j > 0 && f.lines[j-1].kind == kComment {
		j--
	}
	if j == h {
		return h
	}
	if j == 0 || f.lines[j-1].kind == kBlank {
		return j
	}
	return h
}

func (f *File) nextHeader(from int) int {
	for i := from; i < len(f.lines); i++ {
		if f.lines[i].kind == kHeader {
			return i
		}
	}
	return len(f.lines)
}

func (f *File) indexOf(l *line) int {
	for i, x := range f.lines {
		if x == l {
			return i
		}
	}
	return -1
}

// insertAfter inserts a new line after index i, keeping a missing trailing
// newline at the very end of the file if there was one.
func (f *File) insertAfter(i int, nl *line) {
	prev := f.lines[i]
	nl.eol = prev.eol
	if prev.eol == "" {
		prev.eol = f.nl
	}
	f.lines = append(f.lines, nil)
	copy(f.lines[i+2:], f.lines[i+1:])
	f.lines[i+1] = nl
	f.classify()
}

func (f *File) deleteRange(from, to int) {
	if from >= to {
		return
	}
	lastWasEOF := to == len(f.lines)
	eol := f.lines[to-1].eol
	f.lines = append(f.lines[:from], f.lines[to:]...)
	if lastWasEOF && eol == "" && from > 0 {
		f.lines[from-1].eol = "" // keep "no trailing newline" style
	}
	f.classify()
}

// sanitize removes characters that would break the line structure.
func sanitize(s string) string {
	if strings.ContainsAny(s, "\r\n") {
		s = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(s)
	}
	return s
}

// ---- Section ----

// Name returns the normalized header text, e.g. "profile p_x".
func (s *Section) Name() string { return s.hdr.name }

// body returns the half-open line range [from, to) of the section's body, or
// (-1, -1) if the section was removed.
func (s *Section) body() (int, int) {
	h := s.f.indexOf(s.hdr)
	if h < 0 {
		return -1, -1
	}
	return h + 1, s.f.nextHeader(h + 1)
}

// keyEnd returns the index after the key line at i and its continuation lines.
func (s *Section) keyEnd(i, to int) int {
	j := i + 1
	for j < to && s.f.lines[j].kind == kCont {
		j++
	}
	return j
}

func keyValue(raw string) string {
	i := strings.IndexByte(raw, '=')
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(raw[i+1:])
}

// lastKey returns the index of the last line defining key (case-insensitive,
// like the AWS CLI's configparser) or -1.
func (s *Section) lastKey(key string) int {
	from, to := s.body()
	idx := -1
	for i := from; i >= 0 && i < to; i++ {
		l := s.f.lines[i]
		if l.kind == kKey && strings.EqualFold(l.name, key) {
			idx = i
		}
	}
	return idx
}

// Get returns the value of key. With duplicate keys the last one wins (as in
// the AWS CLI). Continuation lines are appended as "\n"+trimmed line.
func (s *Section) Get(key string) (string, bool) {
	i := s.lastKey(key)
	if i < 0 {
		return "", false
	}
	_, to := s.body()
	v := keyValue(s.f.lines[i].raw)
	for j := i + 1; j < s.keyEnd(i, to); j++ {
		v += "\n" + strings.TrimSpace(s.f.lines[j].raw)
	}
	return v, true
}

// Set updates the (last) existing key line in place, preserving its
// indentation and "key = " spacing and dropping its continuation lines; if the
// key is absent a "key = value" line is appended after the section's last key
// line (or right after the header). Newlines in value are replaced by spaces.
func (s *Section) Set(key, value string) {
	from, to := s.body()
	if from < 0 {
		return
	}
	key, value = sanitize(key), sanitize(value)
	if i := s.lastKey(key); i >= 0 {
		l := s.f.lines[i]
		eq := strings.IndexByte(l.raw, '=')
		j := eq + 1
		for j < len(l.raw) && (l.raw[j] == ' ' || l.raw[j] == '\t') {
			j++
		}
		prefix := l.raw[:j]
		if j == eq+1 && eq > 0 && (l.raw[eq-1] == ' ' || l.raw[eq-1] == '\t') && value != "" {
			prefix += " " // "s3 =" → "s3 = value"
		}
		l.raw = prefix + value
		s.f.deleteRange(i+1, s.keyEnd(i, to))
		return
	}
	at := from - 1 // header
	indent := ""
	for i := from; i < to; i++ {
		if k := s.f.lines[i].kind; k == kKey || k == kCont || k == kOther {
			at = i
			if k == kKey {
				indent = leadingWS(s.f.lines[i].raw)
			}
		}
	}
	// The new key must not capture following indented lines as its
	// continuation: indent it at least as deep as the run of lines after it.
	need := 0
	for j := at + 1; j < len(s.f.lines); j++ {
		l := s.f.lines[j]
		if l.kind == kBlank {
			break
		}
		need = max(need, len(leadingWS(l.raw)))
		if l.kind != kComment {
			break
		}
	}
	if need > len(indent) {
		indent = strings.Repeat(" ", need)
	}
	s.f.insertAfter(at, &line{raw: indent + key + " = " + value, kind: kKey, name: key})
}

func leadingWS(s string) string { return s[:len(s)-len(strings.TrimLeft(s, " \t"))] }

// Delete removes every line defining key together with its continuation lines.
func (s *Section) Delete(key string) bool {
	found := false
	for {
		i := s.lastKey(key)
		if i < 0 {
			return found
		}
		_, to := s.body()
		s.f.deleteRange(i, s.keyEnd(i, to))
		found = true
	}
}

// Keys returns the distinct key names of the section in order of appearance.
func (s *Section) Keys() []string {
	from, to := s.body()
	seen := map[string]bool{}
	var out []string
	for i := from; i >= 0 && i < to; i++ {
		l := s.f.lines[i]
		if l.kind == kKey && !seen[strings.ToLower(l.name)] {
			seen[strings.ToLower(l.name)] = true
			out = append(out, l.name)
		}
	}
	return out
}

package awsini

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

var roundTripCases = map[string]string{
	"empty":            "",
	"only newline":     "\n",
	"simple":           "[default]\nregion = eu-west-1\n",
	"no trailing nl":   "[default]\nregion = eu-west-1",
	"crlf":             "[default]\r\nregion = eu-west-1\r\n\r\n[profile a]\r\nx=1\r\n",
	"mixed eol":        "[a]\r\nx = 1\n\n[b]\ny = 2\r\n",
	"lone cr":          "[a]\rx = 1\n",
	"comments":         "# top\n; semi\n[a]\n# inside\nx = 1 ; not a comment for us\n",
	"keys before":      "global = 1\n\n[a]\nx = 1\n",
	"weird spacing":    "[ profile x ]\n  key   =    value   \nk2=v2\nk3 =v3\nk4= v4\n",
	"duplicate keys":   "[a]\nx = 1\nx = 2\n",
	"duplicate sects":  "[a]\nx = 1\n[a]\nx = 2\n",
	"nested":           "[profile a]\ns3 =\n  max_concurrent_requests = 20\n  max_queue_size = 10000\nregion = eu-west-1\n",
	"bom":              "\uFEFF[default]\nregion = x\n",
	"garbage":          "this is not ini\n[unterminated\n]\n=\n==\n[]\n",
	"trailing blanks":  "[a]\nx = 1\n\n\n\n",
	"tabs":             "[a]\n\tx\t=\t1\n",
	"header comment":   "[a] # comment\nx = 1\n",
	"unicode":          "[profile ünï]\nname = שלום\n",
	"only comments":    "# a\n# b",
	"whitespace lines": "   \n\t\n[a]\n  \nx = 1\n",
}

func TestRoundTrip(t *testing.T) {
	for name, in := range roundTripCases {
		t.Run(name, func(t *testing.T) {
			f, err := Parse([]byte(in))
			if err != nil {
				t.Fatal(err)
			}
			if got := string(f.Bytes()); got != in {
				t.Fatalf("round trip mismatch\n got %q\nwant %q", got, in)
			}
		})
	}
}

// synthetic200 builds a realistic config with ~200 profiles, comments and
// hand-written content. All IDs/names are fake.
func synthetic200(nl string) string {
	var b strings.Builder
	w := func(s string) { b.WriteString(s + nl) }
	w("# AWS config — managed partly by hand")
	w("; do not edit the sso-session block")
	w("[default]")
	w("region = eu-west-1")
	w("output = json")
	w("")
	w("[sso-session acme]")
	w("sso_start_url = https://acme-fake.awsapps.com/start")
	w("sso_region = eu-west-1")
	w("sso_registration_scopes = sso:account:access")
	w("")
	for i := 0; i < 200; i++ {
		if i%17 == 0 {
			w(fmt.Sprintf("# team %d accounts", i/17))
		}
		w(fmt.Sprintf("[profile p_acme-%03d]", i))
		w("sso_session = acme")
		w(fmt.Sprintf("sso_account_id = %012d", 100000000000+i))
		if i%3 == 0 {
			w("sso_role_name=AdministratorAccess")
		} else {
			w("sso_role_name   =  ReadOnlyAccess")
		}
		w("region = eu-west-1")
		if i%10 == 0 {
			w("s3 =")
			w("  max_concurrent_requests = 20")
		}
		w("output = yaml")
		if i%25 == 0 {
			w("# region = us-east-1")
		}
		w("")
	}
	w("[profile legacy]")
	w("sso_start_url = https://legacy-fake.awsapps.com/start")
	w("sso_region = us-east-1")
	w("sso_account_id = 999999999999")
	w("sso_role_name = Dev")
	b.WriteString("[profile static]" + nl + "credential_process = /bin/true") // no trailing newline
	return b.String()
}

func TestRoundTripSynthetic200(t *testing.T) {
	for _, nl := range []string{"\n", "\r\n"} {
		in := synthetic200(nl)
		f, _ := Parse([]byte(in))
		if string(f.Bytes()) != in {
			t.Fatalf("synthetic round trip mismatch (nl=%q)", nl)
		}
		if got := len(Profiles(f)); got != 203 {
			t.Fatalf("profiles = %d, want 203", got)
		}
		if f.Newline() != nl {
			t.Fatalf("newline = %q", f.Newline())
		}
		s := f.Section("profile p_acme-010")
		if v, _ := s.Get("s3"); v != "\nmax_concurrent_requests = 20" {
			t.Fatalf("nested get = %q", v)
		}
		if v, _ := s.Get("output"); v != "yaml" {
			t.Fatalf("output = %q", v)
		}
		// Edit one profile; everything else must be byte-identical.
		f.Section("profile p_acme-050").Set("sso_role_name", "PowerUser")
		out := string(f.Bytes())
		want := strings.Replace(in, "[profile p_acme-050]"+nl+"sso_session = acme"+nl+"sso_account_id = 100000000050"+nl+"sso_role_name   =  ReadOnlyAccess",
			"[profile p_acme-050]"+nl+"sso_session = acme"+nl+"sso_account_id = 100000000050"+nl+"sso_role_name   =  PowerUser", 1)
		if out != want {
			t.Fatalf("single-key edit changed other bytes")
		}
		ss := SSOSessions(f)
		if len(ss) != 1 || ss[0].Name != "acme" || ss[0].Scopes != "sso:account:access" || ss[0].Region != "eu-west-1" {
			t.Fatalf("sessions = %+v", ss)
		}
	}
}

func TestSectionLookup(t *testing.T) {
	f, _ := Parse([]byte("[ profile   x ]\na = 1\n[profile x]\na = 2\n[default]\n"))
	s := f.Section("profile x")
	if s == nil {
		t.Fatal("not found")
	}
	if v, _ := s.Get("a"); v != "1" {
		t.Fatalf("first duplicate should win, got %q", v)
	}
	if n := len(f.SectionsNamed("profile x")); n != 2 {
		t.Fatalf("SectionsNamed = %d", n)
	}
	if d := f.Duplicates(); len(d) != 1 || d[0] != "profile x" {
		t.Fatalf("Duplicates = %v", d)
	}
	if len(f.Sections()) != 3 {
		t.Fatal("Sections count")
	}
	if f.Section("nope") != nil {
		t.Fatal("unexpected section")
	}
	if f.Section(" profile  x") == nil {
		t.Fatal("query should be normalized too")
	}
}

func TestGet(t *testing.T) {
	f, _ := Parse([]byte("[a]\nx = 1\nX = 2\nempty =\nspaced   =   v w  \n"))
	s := f.Section("a")
	cases := []struct {
		key, want string
		ok        bool
	}{
		{"x", "2", true}, // last duplicate wins, case-insensitive
		{"empty", "", true},
		{"spaced", "v w", true},
		{"missing", "", false},
	}
	for _, c := range cases {
		v, ok := s.Get(c.key)
		if v != c.want || ok != c.ok {
			t.Errorf("Get(%q) = %q,%v want %q,%v", c.key, v, ok, c.want, c.ok)
		}
	}
	if k := s.Keys(); strings.Join(k, ",") != "x,empty,spaced" {
		t.Fatalf("Keys = %v", k)
	}
}

func TestSet(t *testing.T) {
	cases := []struct {
		name, in, sect, key, val, want string
	}{
		{"update keeps spacing", "[a]\nk   =  old\n", "a", "k", "new", "[a]\nk   =  new\n"},
		{"update no spaces", "[a]\nk=old\n", "a", "k", "new", "[a]\nk=new\n"},
		{"update indented", "[a]\n  k = old\n", "a", "k", "new", "[a]\n  k = new\n"},
		{"update empty value", "[a]\nk =\n", "a", "k", "v", "[a]\nk = v\n"},
		{"update last dup", "[a]\nk = 1\nk = 2\n", "a", "k", "3", "[a]\nk = 1\nk = 3\n"},
		{"update drops continuation", "[a]\ns3 =\n  x = 1\nk = 2\n", "a", "s3", "v", "[a]\ns3 = v\nk = 2\n"},
		{"append after last key", "[a]\nx = 1\n\n# about b\n[b]\ny = 2\n", "a", "k", "v", "[a]\nx = 1\nk = v\n\n# about b\n[b]\ny = 2\n"},
		{"append after nested block", "[a]\ns3 =\n  x = 1\n\n[b]\n", "a", "k", "v", "[a]\ns3 =\n  x = 1\nk = v\n\n[b]\n"},
		{"append to empty section", "[a]\n\n[b]\n", "a", "k", "v", "[a]\nk = v\n\n[b]\n"},
		{"append at eof no newline", "[a]\nx = 1", "a", "k", "v", "[a]\nx = 1\nk = v"},
		{"append crlf", "[a]\r\nx = 1\r\n", "a", "k", "v", "[a]\r\nx = 1\r\nk = v\r\n"},
		{"append header only no nl", "[a]", "a", "k", "v", "[a]\nk = v"},
		{"case insensitive", "[a]\nRegion = x\n", "a", "region", "y", "[a]\nRegion = y\n"},
		{"newline injection", "[a]\n", "a", "k", "v\n[evil]", "[a]\nk = v [evil]\n"},
		{"append keeps indentation", "[a]\n  x = 1\n", "a", "k", "v", "[a]\n  x = 1\n  k = v\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _ := Parse([]byte(c.in))
			f.Section(c.sect).Set(c.key, c.val)
			if got := string(f.Bytes()); got != c.want {
				t.Fatalf("got %q\nwant %q", got, c.want)
			}
			assertStable(t, f)
		})
	}
}

func TestDelete(t *testing.T) {
	cases := []struct {
		name, in, key, want string
		ok                  bool
	}{
		{"simple", "[a]\nx = 1\ny = 2\n", "x", "[a]\ny = 2\n", true},
		{"with continuation", "[a]\ns3 =\n  m = 1\n  n = 2\ny = 2\n", "s3", "[a]\ny = 2\n", true},
		{"duplicates", "[a]\nx = 1\ny = 2\nx = 3\n", "x", "[a]\ny = 2\n", true},
		{"missing", "[a]\nx = 1\n", "z", "[a]\nx = 1\n", false},
		{"only other section", "[a]\nx = 1\n[b]\nx = 2\n", "x", "[a]\n[b]\nx = 2\n", true},
		{"last line no nl", "[a]\ny = 2\nx = 1", "x", "[a]\ny = 2", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _ := Parse([]byte(c.in))
			if ok := f.Section("a").Delete(c.key); ok != c.ok {
				t.Fatalf("Delete ok = %v", ok)
			}
			if got := string(f.Bytes()); got != c.want {
				t.Fatalf("got %q\nwant %q", got, c.want)
			}
			assertStable(t, f)
		})
	}
}

func TestAddSection(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"empty file", "", "[n]\nk = v\n"},
		{"no trailing nl", "[a]\nx = 1", "[a]\nx = 1\n\n[n]\nk = v\n"},
		{"trailing nl", "[a]\nx = 1\n", "[a]\nx = 1\n\n[n]\nk = v\n"},
		{"one blank", "[a]\nx = 1\n\n", "[a]\nx = 1\n\n[n]\nk = v\n"},
		{"many blanks", "[a]\nx = 1\n\n\n\n", "[a]\nx = 1\n\n[n]\nk = v\n"},
		{"crlf", "[a]\r\nx = 1\r\n", "[a]\r\nx = 1\r\n\r\n[n]\r\nk = v\r\n"},
		{"trailing comment", "[a]\nx = 1\n# end", "[a]\nx = 1\n# end\n\n[n]\nk = v\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _ := Parse([]byte(c.in))
			f.AddSection("n").Set("k", "v")
			if got := string(f.Bytes()); got != c.want {
				t.Fatalf("got %q\nwant %q", got, c.want)
			}
			assertStable(t, f)
		})
	}
	f, _ := Parse([]byte("[a]\nx = 1\n"))
	if s := f.AddSection("a"); s == nil || string(f.Bytes()) != "[a]\nx = 1\n" {
		t.Fatal("AddSection of existing name must return it unchanged")
	}
}

func TestRemoveSection(t *testing.T) {
	cases := []struct{ name, in, sect, want string }{
		{"middle", "[a]\nx = 1\n\n[b]\ny = 2\n\n[c]\nz = 3\n", "b", "[a]\nx = 1\n\n[c]\nz = 3\n"},
		{"first", "[a]\nx = 1\n\n[b]\ny = 2\n", "a", "[b]\ny = 2\n"},
		{"last", "[a]\nx = 1\n\n[b]\ny = 2\n", "b", "[a]\nx = 1\n"},
		{"last no nl", "[a]\nx = 1\n\n[b]\ny = 2", "b", "[a]\nx = 1\n"},
		{"leading comment removed", "[a]\nx = 1\n\n# about b\n# more\n[b]\ny = 2\n\n[c]\n", "b", "[a]\nx = 1\n\n[c]\n"},
		{"comment attached to prev key kept", "[a]\nx = 1\n# x note\n[b]\ny = 2\n\n[c]\n", "b", "[a]\nx = 1\n# x note\n[c]\n"},
		{"next section comment kept", "[a]\nx = 1\n\n# about b\n[b]\ny = 2\n", "a", "# about b\n[b]\ny = 2\n"},
		{"next comment directly after keys goes with section", "[a]\nx = 1\n# trailing\n[b]\n", "a", "[b]\n"},
		{"commented key in body", "[a]\nx = 1\n# old = 2\n\n[b]\n", "a", "[b]\n"},
		{"nested body", "[a]\ns3 =\n  m = 1\n\n[b]\n", "a", "[b]\n"},
		{"keys before first section kept", "g = 1\n\n[a]\nx = 1\n\n[b]\n", "a", "g = 1\n\n[b]\n"},
		{"top comment of file", "# header\n[a]\nx = 1\n\n[b]\n", "a", "[b]\n"},
		{"duplicates remove first", "[a]\nx = 1\n\n[a]\nx = 2\n", "a", "[a]\nx = 2\n"},
		{"crlf", "[a]\r\nx = 1\r\n\r\n[b]\r\ny = 2\r\n\r\n[c]\r\n", "b", "[a]\r\nx = 1\r\n\r\n[c]\r\n"},
		{"triple blank collapse at eof", "[a]\nx = 1\n\n\n[b]\ny = 2\n\n\n", "b", "[a]\nx = 1\n"},
		{"only section", "[a]\nx = 1\n", "a", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _ := Parse([]byte(c.in))
			if !f.RemoveSection(c.sect) {
				t.Fatal("RemoveSection returned false")
			}
			got := string(f.Bytes())
			if got != c.want {
				t.Fatalf("got %q\nwant %q", got, c.want)
			}
			if strings.Contains(got, "\n\n\n") {
				t.Fatal("triple blank lines")
			}
			assertStable(t, f)
		})
	}
	f, _ := Parse([]byte("[a]\n"))
	if f.RemoveSection("zz") {
		t.Fatal("removing missing section should return false")
	}
	s := f.Section("a")
	f.Remove(s)
	s.Set("k", "v") // no-op, must not panic
	if _, ok := s.Get("k"); ok || len(f.Bytes()) != 0 {
		t.Fatal("removed section should be inert")
	}
}

func TestProfiles(t *testing.T) {
	in := `[default]
sso_session = s
sso_account_id = 111111111111
sso_role_name = Admin
region = eu-west-1
output = yaml

[profile p_acme-dev]
sso_session = s
sso_account_id = 222222222222
sso_role_name = ReadOnly

[profile legacy]
sso_start_url = https://fake.awsapps.com/start
sso_region = us-east-1
sso_account_id = 333333333333
sso_role_name = Dev

[profile static]
aws_access_key_id = nope

[sso-session s]
sso_start_url = https://fake2.awsapps.com/start
sso_region = eu-west-1

[services x]
foo = bar
`
	f, _ := Parse([]byte(in))
	ps := Profiles(f)
	if len(ps) != 4 {
		t.Fatalf("got %d profiles: %+v", len(ps), ps)
	}
	if ps[0].Name != "default" || ps[0].Region != "eu-west-1" || ps[0].Output != "yaml" || !ps[0].IsSSO() {
		t.Fatalf("default = %+v", ps[0])
	}
	if ps[1].Name != "p_acme-dev" || ps[1].AccountID != "222222222222" || ps[1].Session != "s" {
		t.Fatalf("p = %+v", ps[1])
	}
	if ps[2].LegacyStartURL != "https://fake.awsapps.com/start" || ps[2].LegacyRegion != "us-east-1" || !ps[2].IsSSO() {
		t.Fatalf("legacy = %+v", ps[2])
	}
	if ps[3].IsSSO() {
		t.Fatal("static profile should not be SSO")
	}
	ss := SSOSessions(f)
	if len(ss) != 1 || ss[0].StartURL != "https://fake2.awsapps.com/start" {
		t.Fatalf("sessions = %+v", ss)
	}
	if ProfileSectionName("default") != "default" || ProfileSectionName("x") != "profile x" {
		t.Fatal("ProfileSectionName")
	}
}

// assertStable checks that the in-memory state equals a fresh parse of Bytes.
func assertStable(t *testing.T, f *File) {
	t.Helper()
	b := f.Bytes()
	g, _ := Parse(b)
	if !bytes.Equal(g.Bytes(), b) {
		t.Fatal("reparse not identical")
	}
	if len(g.Sections()) != len(f.Sections()) {
		t.Fatalf("section count changed on reparse: %d vs %d", len(g.Sections()), len(f.Sections()))
	}
	for i, s := range f.Sections() {
		gs := g.Sections()[i]
		if s.Name() != gs.Name() || strings.Join(s.Keys(), ",") != strings.Join(gs.Keys(), ",") {
			t.Fatalf("section %d differs after reparse", i)
		}
	}
}

func FuzzRoundTrip(f *testing.F) {
	for _, c := range roundTripCases {
		f.Add([]byte(c))
	}
	f.Add([]byte(synthetic200("\n")))
	f.Add([]byte(synthetic200("\r\n")))
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(p.Bytes(), data) {
			t.Fatalf("round trip mismatch for %q", data)
		}
		// Edits must never panic and must leave a self-consistent file.
		secs := p.Sections()
		if len(secs) > 0 {
			secs[0].Set("fuzz_key", "v")
			if v, ok := secs[0].Get("fuzz_key"); !ok || v != "v" {
				t.Fatalf("Set/Get mismatch: %q %v", v, ok)
			}
			secs[0].Delete("fuzz_key")
		}
		p.AddSection("fuzz new").Set("a", "b")
		if s := p.Section("fuzz new"); s == nil {
			t.Fatal("added section missing")
		}
		p.RemoveSection("fuzz new")
		if len(secs) > 1 {
			p.Remove(secs[1])
		}
		assertStable(t, p)
	})
}

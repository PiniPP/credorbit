package awsini

import (
	"strings"

	"github.com/credorbit/credorbit/internal/model"
)

// ProfileSectionName returns the config-file section name of a profile:
// "default" → "default", anything else → "profile "+name.
func ProfileSectionName(profile string) string {
	if profile == "default" {
		return "default"
	}
	return "profile " + profile
}

// ProfileNameFromSection is the inverse of ProfileSectionName. ok is false for
// sections that are not profiles (sso-session, services, ...).
func ProfileNameFromSection(section string) (name string, ok bool) {
	section = normalize(section)
	if section == "default" {
		return "default", true
	}
	if rest, found := strings.CutPrefix(section, "profile "); found && rest != "" {
		return rest, true
	}
	return "", false
}

// Profiles returns every "[profile X]" section and "[default]" as a
// model.Profile, in file order. Non-SSO profiles are included too (filter with
// Profile.IsSSO). For duplicate headers only the first occurrence is used.
func Profiles(f *File) []model.Profile {
	var out []model.Profile
	seen := map[string]bool{}
	for _, s := range f.Sections() {
		name, ok := ProfileNameFromSection(s.Name())
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		get := func(k string) string { v, _ := s.Get(k); return v }
		out = append(out, model.Profile{
			Name:           name,
			Session:        get("sso_session"),
			AccountID:      get("sso_account_id"),
			Role:           get("sso_role_name"),
			Region:         get("region"),
			Output:         get("output"),
			LegacyStartURL: get("sso_start_url"),
			LegacyRegion:   get("sso_region"),
		})
	}
	return out
}

// SSOSessions returns every "[sso-session X]" section (first occurrence wins).
func SSOSessions(f *File) []model.SSOSession {
	var out []model.SSOSession
	seen := map[string]bool{}
	for _, s := range f.Sections() {
		name, ok := strings.CutPrefix(s.Name(), "sso-session ")
		if !ok || name == "" || seen[name] {
			continue
		}
		seen[name] = true
		get := func(k string) string { v, _ := s.Get(k); return v }
		out = append(out, model.SSOSession{
			Name:     name,
			StartURL: get("sso_start_url"),
			Region:   get("sso_region"),
			Scopes:   get("sso_registration_scopes"),
		})
	}
	return out
}

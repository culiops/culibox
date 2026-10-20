package awshost

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Profile is one profile of the host's AWS config file, reduced to what the
// credentials screen needs to pick a signer. It deliberately has no field for
// static keys: those are never copied out of the file.
type Profile struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"` // "sso" | "assume-role" | "other"
	SSOAccountID  string `json:"sso_account_id,omitempty"`
	SSORoleName   string `json:"sso_role_name,omitempty"`
	SSOSession    string `json:"sso_session,omitempty"`
	RoleARN       string `json:"role_arn,omitempty"`
	SourceProfile string `json:"source_profile,omitempty"`
	Region        string `json:"region,omitempty"`
	Selectable    bool   `json:"selectable"`
	Note          string `json:"note,omitempty"`
}

const (
	kindSSO        = "sso"
	kindAssumeRole = "assume-role"
	kindOther      = "other"

	// maxConfigBytes bounds how much of the config file is read.
	maxConfigBytes = 4 << 20

	noteMFA     = "needs an MFA code; not supported"
	noteBadName = "profile name not supported (letters, digits, . _ - only)"
)

var profileNameOK = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// rawProfile holds only the keys the parser looks at, so no other value
// (notably aws_access_key_id / aws_secret_access_key) is ever kept.
type rawProfile struct {
	name string
	kv   map[string]string
}

// HostProfiles lists the profiles of the host's AWS config file (the first
// AWS_CONFIG_FILE entry, else ~/.aws/config). A missing file is an empty list,
// not an error. The shared credentials file is never read.
func HostProfiles() ([]Profile, error) {
	var path string
	if cfgs, _ := SharedFiles(); len(cfgs) > 0 {
		path = cfgs[0]
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil
		}
		path = filepath.Join(home, ".aws", "config")
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	return ParseProfiles(io.LimitReader(f, maxConfigBytes))
}

// ParseProfiles reads AWS config-file text and returns its profiles in file
// order. It is a plain line parser: it executes nothing a profile names
// (credential_process, web_identity_token_file stay inert strings) and keeps
// only the handful of keys in wantKeys.
func ParseProfiles(r io.Reader) ([]Profile, error) {
	wantKeys := map[string]bool{
		"sso_account_id": true, "sso_role_name": true, "sso_session": true,
		"role_arn": true, "source_profile": true, "region": true, "mfa_serial": true,
	}
	var (
		order  []*rawProfile
		byName = map[string]*rawProfile{}
		cur    *rawProfile // nil outside a profile section
	)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		raw := strings.TrimRight(sc.Text(), "\r")
		if raw != strings.TrimLeft(raw, " \t") {
			continue // indented: a sub-block entry (s3 = ... / max_concurrent_requests = 5)
		}
		line := strings.TrimSpace(raw)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			cur = nil
			end := strings.IndexByte(line, ']')
			if end < 0 {
				continue
			}
			name, ok := profileSectionName(strings.TrimSpace(line[1:end]))
			if !ok {
				continue // sso-session, services, malformed
			}
			if p, seen := byName[name]; seen {
				cur = p
			} else {
				cur = &rawProfile{name: name, kv: map[string]string{}}
				byName[name] = cur
				order = append(order, cur)
			}
			continue
		}
		if cur == nil {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		if wantKeys[k] {
			cur.kv[k] = cleanValue(v)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	var out []Profile
	for _, rp := range order {
		out = append(out, build(rp))
	}
	return out, nil
}

// profileSectionName maps a section header body to a profile name; ok is false
// for sections that are not profiles.
func profileSectionName(h string) (string, bool) {
	if h == "default" {
		return "default", true
	}
	rest, found := strings.CutPrefix(h, "profile")
	if !found || rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return "", false
	}
	name := strings.TrimSpace(rest)
	return name, name != ""
}

// cleanValue trims a value and drops a trailing " #..." / " ;..." comment.
func cleanValue(v string) string {
	v = strings.TrimSpace(v)
	for i := 1; i < len(v); i++ {
		if (v[i] == '#' || v[i] == ';') && (v[i-1] == ' ' || v[i-1] == '\t') {
			return strings.TrimSpace(v[:i])
		}
	}
	return v
}

func build(rp *rawProfile) Profile {
	p := Profile{
		Name:          rp.name,
		Kind:          kindOther,
		SSOAccountID:  rp.kv["sso_account_id"],
		SSORoleName:   rp.kv["sso_role_name"],
		SSOSession:    rp.kv["sso_session"],
		RoleARN:       rp.kv["role_arn"],
		SourceProfile: rp.kv["source_profile"],
		Region:        rp.kv["region"],
		Selectable:    true,
	}
	switch {
	case p.RoleARN != "" && p.SourceProfile != "":
		p.Kind = kindAssumeRole
		p.SSOAccountID, p.SSORoleName, p.SSOSession = "", "", ""
	case p.SSOAccountID != "" && p.SSORoleName != "":
		p.Kind = kindSSO
		p.RoleARN, p.SourceProfile = "", ""
	default:
		// credential_process, web_identity_token_file, static keys, anything
		// else: the SDK resolves these at mint time. Show no half-fields.
		p.SSOAccountID, p.SSORoleName, p.SSOSession = "", "", ""
		p.RoleARN, p.SourceProfile = "", ""
	}
	switch {
	case !profileNameOK.MatchString(p.Name):
		p.Selectable, p.Note = false, noteBadName
	case rp.kv["mfa_serial"] != "":
		p.Selectable, p.Note = false, noteMFA
	}
	return p
}

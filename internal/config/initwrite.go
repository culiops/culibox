package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// InitEdit is the set of config keys `culibox init` owns. Everything else in
// the file is kept as it is.
type InitEdit struct {
	DefaultCwd  string // always set by init
	VMCPUs      int    // 0 = leave the key as it is
	VMMemoryGiB int    // 0 = leave
	Account     string // "" = leave agents' account keys alone
}

// claudeBin is the binary name an init-owned agent entry runs.
const claudeBin = "claude"

// initDoc is a config file split just far enough to edit the keys init owns:
// the top level and the agents list are maps of raw values, so every key (and
// every agent key) init does not touch round-trips untouched.
type initDoc struct {
	top    map[string]json.RawMessage
	agents []map[string]json.RawMessage
}

func parseInitDoc(b []byte) (initDoc, error) {
	var d initDoc
	if err := json.Unmarshal(b, &d.top); err != nil {
		return initDoc{}, fmt.Errorf("config is not a JSON object: %w", err)
	}
	if d.top == nil { // the JSON literal null
		return initDoc{}, errors.New("config is not a JSON object")
	}
	if raw, ok := d.top["agents"]; ok {
		if err := json.Unmarshal(raw, &d.agents); err != nil {
			return initDoc{}, fmt.Errorf("config agents: %w", err)
		}
	}
	return d, nil
}

// render writes the document: keys sorted, two-space indent, one trailing
// newline, no HTML escaping (a value must come out as it went in).
func (d initDoc) render() ([]byte, error) {
	top := make(map[string]json.RawMessage, len(d.top)+1)
	for k, v := range d.top {
		top[k] = v
	}
	if d.agents != nil {
		raw, err := marshalPlain(d.agents)
		if err != nil {
			return nil, err
		}
		top["agents"] = raw
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(top); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func marshalPlain(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func rawJSON(v any) json.RawMessage {
	raw, err := marshalPlain(v)
	if err != nil { // only strings and ints go in here
		panic(err)
	}
	return raw
}

// rawStr is the string value of a raw JSON value, "" when absent or not a string.
func rawStr(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// seedInitDoc is the file init creates when there is none: only the bind
// address and the claude agent, both taken from Defaults().
func seedInitDoc() initDoc {
	def := Defaults()
	d := initDoc{top: map[string]json.RawMessage{"bind_addr": rawJSON(def.BindAddr)}}
	d.agents = []map[string]json.RawMessage{defaultClaudeEntry()}
	return d
}

func defaultClaudeEntry() map[string]json.RawMessage {
	name, bin := claudeBin, claudeBin
	for _, a := range Defaults().Agents {
		if a.Name == claudeBin {
			name, bin = a.Name, a.Bin
			break
		}
	}
	return map[string]json.RawMessage{"name": rawJSON(name), "bin": rawJSON(bin)}
}

// PlanInitWrite returns the file content init would write. existing == nil
// means there is no file yet. It sets default_runtime "box", default_cwd,
// box_vm_cpus and box_vm_memory_gib (when non-zero), and the account of every
// claude agent that has none. A claude agent already on another account is
// left alone and its name is returned in skipped. With no claude agent at all,
// one is appended. The result must parse and Validate the way Load plus
// Validate would treat it, else err.
func PlanInitWrite(existing []byte, e InitEdit) (out []byte, skipped []string, err error) {
	d := seedInitDoc()
	if existing != nil {
		if d, err = parseInitDoc(existing); err != nil {
			return nil, nil, err
		}
	}
	d.top["default_runtime"] = rawJSON(RuntimeBox)
	d.top["default_cwd"] = rawJSON(e.DefaultCwd)
	if e.VMCPUs != 0 {
		d.top["box_vm_cpus"] = rawJSON(e.VMCPUs)
	}
	if e.VMMemoryGiB != 0 {
		d.top["box_vm_memory_gib"] = rawJSON(e.VMMemoryGiB)
	}

	hasClaude := false
	for _, a := range d.agents {
		// The claude agent is found by name or by binary name, the same rule
		// init's dependency step uses to pick the binary it runs.
		if rawStr(a["name"]) != claudeBin && filepath.Base(rawStr(a["bin"])) != claudeBin {
			continue
		}
		hasClaude = true
		if e.Account == "" {
			continue
		}
		switch cur := rawStr(a["account"]); cur {
		case "":
			a["account"] = rawJSON(e.Account)
		case e.Account:
		default:
			skipped = append(skipped, rawStr(a["name"]))
		}
	}
	if !hasClaude {
		a := defaultClaudeEntry()
		if e.Account != "" {
			a["account"] = rawJSON(e.Account)
		}
		d.agents = append(d.agents, a)
	}

	out, err = d.render()
	if err != nil {
		return nil, nil, err
	}
	c, err := parseConfig(out)
	if err == nil {
		err = c.Validate()
	}
	if err != nil {
		return nil, nil, fmt.Errorf("the edited config would be invalid: %w", err)
	}
	return out, skipped, nil
}

// NormalizeJSON re-renders an existing config the way PlanInitWrite does, so a
// diff between the two shows only the real changes.
func NormalizeJSON(b []byte) ([]byte, error) {
	d, err := parseInitDoc(b)
	if err != nil {
		return nil, err
	}
	return d.render()
}

// WriteFileAtomic replaces path with data: 0600, temp file in the same
// directory, fsync, rename, fsync of the directory. The parent is created 0700
// when missing. A path that is a symlink is refused, so a write never lands
// somewhere the config does not live.
func WriteFileAtomic(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if fi, lerr := os.Lstat(path); lerr == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; refusing to write through it", path)
		}
	} else if !errors.Is(lerr, os.ErrNotExist) {
		return lerr
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close() // a second Close is harmless here
			_ = os.Remove(tmp)
		}
	}()
	if err = f.Chmod(0o600); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	// The rename is done: a failure to sync the directory is reported, but the
	// new file is in place and the temp name is gone.
	d, derr := os.Open(dir)
	if derr != nil {
		return derr
	}
	defer d.Close()
	return d.Sync()
}

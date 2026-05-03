// Package config implements remolo's on-disk configuration and host-alias
// system. The config file maps short alias names to saved session tokens
// (and default flags), so users can run "remolo connect workstation" instead
// of pasting a long token every time.
//
// The file lives at $XDG_CONFIG_HOME/remolo/config.json (falling back to
// $HOME/.config/remolo/config.json) and is encoded as plain JSON to avoid
// pulling in any new dependency.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Config is the root document persisted to disk.
type Config struct {
	Aliases  map[string]Alias    `json:"aliases"`
	Defaults Defaults            `json:"defaults"`
	Groups   map[string][]string `json:"groups,omitempty"`
}

// Alias maps a short name to a saved session token plus optional per-alias
// overrides for the rendezvous and relay endpoints.
type Alias struct {
	Token      string `json:"token,omitempty"`
	Rendezvous string `json:"rendezvous,omitempty"`
	Relay      string `json:"relay,omitempty"`
	Note       string `json:"note,omitempty"`

	// Enrolled-host descriptor (set by 'remolo enroll'): lets the alias connect
	// with the client key instead of a token, even after the token expires.
	HostKey    string   `json:"host_key,omitempty"`   // base64 Ed25519 host pubkey
	Candidates []string `json:"candidates,omitempty"` // host endpoints
}

// Defaults holds connection defaults applied when an alias does not override
// them.
type Defaults struct {
	Rendezvous string `json:"rendezvous,omitempty"`
	Relay      string `json:"relay,omitempty"`
}

// Path returns the absolute path to the config file. It honors
// $XDG_CONFIG_HOME and otherwise falls back to $HOME/.config. If neither is
// resolvable the path is computed relative to the current directory, which
// keeps Path total (it never panics) while still being a sane default.
func Path() string {
	return filepath.Join(dir(), "config.json")
}

// dir returns the directory that contains the config file.
func dir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			base = filepath.Join(home, ".config")
		} else {
			base = ".config"
		}
	}
	return filepath.Join(base, "remolo")
}

// Load reads and parses the config file. A missing file is not an error: it
// yields an empty, ready-to-use Config. The parent directory is not created
// here; that happens lazily on Save.
func Load() (*Config, error) {
	c := &Config{Aliases: map[string]Alias{}}

	data, err := os.ReadFile(Path())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return c, nil
		}
		return nil, fmt.Errorf("config: read %s: %w", Path(), err)
	}

	if len(data) == 0 {
		return c, nil
	}

	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", Path(), err)
	}
	if c.Aliases == nil {
		c.Aliases = map[string]Alias{}
	}
	return c, nil
}

// Save atomically writes the config to disk with 0600 permissions, creating
// the parent directory as needed. It writes to a temp file in the same
// directory and renames it into place so a crash mid-write cannot corrupt an
// existing config.
func (c *Config) Save() error {
	d := dir()
	if err := os.MkdirAll(d, 0o700); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", d, err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(d, "config-*.json.tmp")
	if err != nil {
		return fmt.Errorf("config: temp file: %w", err)
	}
	tmpName := tmp.Name()

	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("config: chmod temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("config: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("config: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("config: close temp: %w", err)
	}

	if err := os.Rename(tmpName, Path()); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("config: rename into place: %w", err)
	}
	return nil
}

// SetAlias stores (or replaces) the alias under the given name.
func (c *Config) SetAlias(name string, a Alias) {
	if c.Aliases == nil {
		c.Aliases = map[string]Alias{}
	}
	c.Aliases[name] = a
}

// RemoveAlias deletes the named alias and reports whether it existed.
func (c *Config) RemoveAlias(name string) bool {
	if c.Aliases == nil {
		return false
	}
	if _, ok := c.Aliases[name]; !ok {
		return false
	}
	delete(c.Aliases, name)
	return true
}

// GetAlias returns the named alias and whether it was found.
func (c *Config) GetAlias(name string) (Alias, bool) {
	if c.Aliases == nil {
		return Alias{}, false
	}
	a, ok := c.Aliases[name]
	return a, ok
}

// AliasNames returns the alias names sorted alphabetically.
func (c *Config) AliasNames() []string {
	names := make([]string, 0, len(c.Aliases))
	for name := range c.Aliases {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Group returns the alias-name list stored under the given group and whether
// the group exists. This is storage only; fleet-exec logic lives elsewhere.
func (c *Config) Group(name string) ([]string, bool) {
	if c.Groups == nil {
		return nil, false
	}
	members, ok := c.Groups[name]
	return members, ok
}

// Resolve interprets arg as either a saved alias name or a raw token. If arg
// matches a saved alias, its token and a pointer to a copy of the alias are
// returned. Otherwise arg is treated as the token itself and alias is nil.
//
// Resolve loads the config from disk; callers that already hold a *Config can
// use ResolveWith to avoid a second read.
func Resolve(arg string) (token string, alias *Alias, err error) {
	c, err := Load()
	if err != nil {
		return "", nil, err
	}
	token, alias = c.ResolveWith(arg)
	return token, alias, nil
}

// ResolveWith is the in-memory form of Resolve against an already-loaded
// Config. If arg names a saved alias, its token and a pointer to a copy of
// the alias are returned; otherwise arg is the token and alias is nil.
func (c *Config) ResolveWith(arg string) (token string, alias *Alias) {
	if a, ok := c.GetAlias(arg); ok {
		cp := a
		return a.Token, &cp
	}
	return arg, nil
}

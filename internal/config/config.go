// Package config loads and persists bakku's TOML configuration (dest
// definitions) and resolves the repository password from the environment, a
// password file, or an interactive prompt.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
	"golang.org/x/term"

	"github.com/zephel01/bakku/internal/notify"
)

// Dest maps a friendly name to a backend URL.
type Dest struct {
	Name string `toml:"name"`
	URL  string `toml:"url"`
}

// Config is the top-level TOML config.
type Config struct {
	Dests  []Dest        `toml:"dest"`
	Notify notify.Config `toml:"notify"`

	// path is where this config was loaded from / will be saved to.
	path string `toml:"-"`
}

// DefaultPath returns the config path, honoring BAKKU_CONFIG, else
// ~/.config/bakku/config.toml.
func DefaultPath() string {
	if p := os.Getenv("BAKKU_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.toml"
	}
	return filepath.Join(home, ".config", "bakku", "config.toml")
}

// Load reads the config from path (DefaultPath if empty). A missing file yields
// an empty config (not an error).
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultPath()
	}
	c := &Config{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return nil, err
	}
	if err := toml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	c.path = path
	return c, nil
}

// Save writes the config back to its path, creating parent directories.
func (c *Config) Save() error {
	if c.path == "" {
		c.path = DefaultPath()
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	data, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, data, 0o600)
}

// Path returns the config file path.
func (c *Config) Path() string { return c.path }

// ResolveDest returns the backend URL for a --repo value: if it names a
// configured dest, its URL is returned; otherwise the value is treated as a
// direct URL/path.
func (c *Config) ResolveDest(repoFlag string) (string, error) {
	if repoFlag == "" {
		return "", errors.New("config: --repo is required")
	}
	for _, d := range c.Dests {
		if d.Name == repoFlag {
			return d.URL, nil
		}
	}
	// Looks like a name (no scheme, no slash) but not found -> helpful error.
	if !strings.Contains(repoFlag, "://") && !strings.ContainsAny(repoFlag, "/\\") {
		return "", fmt.Errorf("config: no dest named %q (add it with `bakku dest add`)", repoFlag)
	}
	return repoFlag, nil
}

// AddDest adds or replaces a dest.
func (c *Config) AddDest(name, url string) {
	for i := range c.Dests {
		if c.Dests[i].Name == name {
			c.Dests[i].URL = url
			return
		}
	}
	c.Dests = append(c.Dests, Dest{Name: name, URL: url})
}

// RemoveDest removes a dest by name, returning whether it existed.
func (c *Config) RemoveDest(name string) bool {
	for i := range c.Dests {
		if c.Dests[i].Name == name {
			c.Dests = append(c.Dests[:i], c.Dests[i+1:]...)
			return true
		}
	}
	return false
}

// PasswordOptions control how ResolvePassword obtains the repository password.
type PasswordOptions struct {
	// File is a --password-file path (first line used, trailing newline trimmed).
	File string
	// Confirm, when true, prompts twice and requires the entries to match
	// (used for `init`).
	Confirm bool
}

// ResolvePassword obtains the repository password. Precedence:
//  1. BAKKU_PASSWORD environment variable
//  2. --password-file
//  3. interactive terminal prompt
func ResolvePassword(opts PasswordOptions) ([]byte, error) {
	if p := os.Getenv("BAKKU_PASSWORD"); p != "" {
		return []byte(p), nil
	}
	if opts.File != "" {
		data, err := os.ReadFile(opts.File)
		if err != nil {
			return nil, fmt.Errorf("config: read password file: %w", err)
		}
		line := data
		if i := strings.IndexByte(string(data), '\n'); i >= 0 {
			line = data[:i]
		}
		return trimCR(line), nil
	}
	return promptPassword(opts.Confirm)
}

func trimCR(b []byte) []byte {
	return []byte(strings.TrimRight(string(b), "\r\n"))
}

func promptPassword(confirm bool) ([]byte, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, errors.New("config: no password provided (set BAKKU_PASSWORD, use --password-file, or run interactively)")
	}
	fmt.Fprint(os.Stderr, "enter repository password: ")
	pw, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, err
	}
	if len(pw) == 0 {
		return nil, errors.New("config: empty password")
	}
	if confirm {
		fmt.Fprint(os.Stderr, "confirm password: ")
		pw2, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return nil, err
		}
		if string(pw) != string(pw2) {
			return nil, errors.New("config: passwords do not match")
		}
	}
	return pw, nil
}

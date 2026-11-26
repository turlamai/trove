// Package config reads the plugin settings file. The format is the standard
// Unraid plugin cfg (KEY="value"), shared with the PHP settings page — the
// daemon treats /boot as read-only for high-frequency data and writes the
// cfg only when the user changes settings (low frequency, spec §5.3).
package config

import (
	"os"
	"strconv"
	"strings"
)

// Config holds the daemon-relevant settings.
type Config struct {
	Path          string // /boot/config/plugins/trove/trove.cfg
	DataDir       string // persistent job journal directory ("" = read-only mode)
	Concurrency   int    // parallel write jobs, 1..4 (default 1)
	PhysicalWrite bool   // allow writes into /mnt/diskN and pools (default false)
	ProtectedMode string // "deny" | "ack" (default deny)
	ExpertMode    bool   // expose physical roots in the UI (default false)
	ShowHidden    bool   // show dotfiles (default false)
	Language      string // ui hint: "zh-CN" | "en" (default "" = browser)
}

// Defaults returns the safe default configuration.
func Defaults(path string) *Config {
	return &Config{
		Path:          path,
		Concurrency:   1,
		PhysicalWrite: false,
		ProtectedMode: "deny",
		ExpertMode:    false,
		ShowHidden:    false,
	}
}

// Load reads the cfg file; missing file → defaults.
func Load(path string) *Config {
	c := Defaults(path)
	raw, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := cutKV(line)
		if !ok {
			continue
		}
		switch strings.ToUpper(k) {
		case "DATA_DIR":
			c.DataDir = v
		case "CONCURRENCY":
			if n, err := strconv.Atoi(v); err == nil {
				c.Concurrency = n
			}
		case "PHYSICAL_WRITE":
			c.PhysicalWrite = v == "true"
		case "PROTECTED_MODE":
			if v == "ack" {
				c.ProtectedMode = "ack"
			} else {
				c.ProtectedMode = "deny"
			}
		case "EXPERT_MODE":
			c.ExpertMode = v == "true"
		case "SHOW_HIDDEN":
			c.ShowHidden = v == "true"
		case "LANGUAGE":
			c.Language = v
		}
	}
	if c.Concurrency < 1 {
		c.Concurrency = 1
	}
	if c.Concurrency > 4 {
		c.Concurrency = 4
	}
	return c
}

// Save writes the cfg atomically in Unraid's KEY="value" format.
func (c *Config) Save() error {
	var b strings.Builder
	b.WriteString("# Trove plugin configuration\n")
	b.WriteString("# Managed by the Trove settings page and daemon.\n")
	w := func(k, v string) { b.WriteString(k + "=\"" + v + "\"\n") }
	w("DATA_DIR", c.DataDir)
	w("CONCURRENCY", strconv.Itoa(c.Concurrency))
	w("PHYSICAL_WRITE", boolStr(c.PhysicalWrite))
	w("PROTECTED_MODE", c.ProtectedMode)
	w("EXPERT_MODE", boolStr(c.ExpertMode))
	w("SHOW_HIDDEN", boolStr(c.ShowHidden))
	w("LANGUAGE", c.Language)
	tmp := c.Path + ".tmp"
	if err := os.MkdirAll(dirOf(c.Path), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, []byte(b.String()), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, c.Path)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func dirOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i > 0 {
		return p[:i]
	}
	return "."
}

// cutKV parses KEY="value" or KEY=value.
func cutKV(line string) (string, string, bool) {
	i := strings.IndexByte(line, '=')
	if i <= 0 {
		return "", "", false
	}
	k := strings.TrimSpace(line[:i])
	v := strings.TrimSpace(line[i+1:])
	v = strings.TrimPrefix(v, "\"")
	v = strings.TrimSuffix(v, "\"")
	return k, v, true
}

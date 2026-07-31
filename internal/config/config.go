// Package config loads evilcode's TOML configuration and resolves model
// references into live providers (plan.md §16, §1.4).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"evilcode/internal/provider"
)

// Env var overrides. Environment beats the config file, and an explicit flag
// beats both.
const (
	EnvModel      = "EVILCODE_MODEL"
	EnvProvider   = "EVILCODE_PROVIDER"
	EnvConfigPath = "EVILCODE_CONFIG"
	EnvOllamaKey  = "OLLAMA_API_KEY"
)

// ProviderKind selects which wire protocol a provider speaks.
type ProviderKind string

const (
	KindOllama ProviderKind = "ollama"
	KindOpenAI ProviderKind = "openai"
	KindMock   ProviderKind = "mock"
)

// ProviderConfig is one `[[provider]]` block.
type ProviderConfig struct {
	Name    string       `toml:"name"`
	Kind    ProviderKind `toml:"kind"`
	BaseURL string       `toml:"base_url"`

	// APIKeyEnv names the environment variable holding the key, so the key
	// itself never lands in a config file.
	APIKeyEnv string `toml:"api_key_env"`

	// APIKey is an escape hatch for keys that genuinely have nowhere else to
	// live. APIKeyEnv is preferred.
	APIKey string `toml:"api_key"`
}

// ModelConfig is an optional `[[model]]` block carrying per-model overrides
// that the provider cannot report.
type ModelConfig struct {
	// Name matches the bare model name, with or without a @provider suffix.
	Name string `toml:"name"`

	// ContextWindow overrides what the provider claims. Ollama in particular
	// defaults far below a model's real capacity.
	ContextWindow int `toml:"context_window"`

	// LenientToolParse enables the JSON-in-text tool-call fallback for models
	// that emit tool calls as prose. Off by default: it can misfire on ordinary
	// text, so it is opt-in per model.
	LenientToolParse bool `toml:"lenient_tool_parse"`

	// AnchorEdits picks the hash-anchored edit mode for models that handle it
	// (plan.md §17). Classic string-replace remains for those that do not.
	AnchorEdits bool `toml:"anchor_edits"`

	// Vision declares that the model accepts image attachments (§6.6).
	//
	// Declared rather than sniffed from the name: a guess that says no to a
	// capable model is invisible, and one that says yes to a text-only model
	// fails deep inside the provider with a message that explains nothing.
	Vision bool `toml:"vision"`
}

// Roles maps internal call sites to model references with fallback chains
// (plan.md §16). Every internal side-call goes through `smol` so ambient work
// never burns the main model.
type Roles struct {
	Default []string `toml:"default"`
	Smol    []string `toml:"smol"`
	Plan    []string `toml:"plan"`
	Commit  []string `toml:"commit"`
}

// Display holds presentation preferences.
type Display struct {
	Centered        bool   `toml:"centered"`
	Theme           string `toml:"theme"`
	KeybindingHints bool   `toml:"keybinding_hints"`
	IdleAnimation   bool   `toml:"idle_animation"`
	Overscroll      string `toml:"overscroll"` // off | on | overscroll
	ThinkingDisplay string `toml:"thinking_display"`

	// ThinkingLines caps a live thinking trace's height before it scrolls in
	// place. Zero uses the built-in default.
	ThinkingLines int `toml:"thinking_lines"`

	// KeepThinking leaves a finished trace expanded instead of collapsing it
	// to `▸ thought (N lines)` when the answer starts.
	KeepThinking bool `toml:"keep_thinking"`

	// InlineDiffs shows a diff under an edit in the transcript. On by default;
	// off is for anyone who would rather read the summary and open the file.
	InlineDiffs     bool `toml:"inline_diffs"`
	ShowToolDetails bool `toml:"show_tool_call_details"`
}

// Features gates optional behavior.
type Features struct {
	AutoPoke bool `toml:"auto_poke"`
	Memory   bool `toml:"memory"`
	Advisor  bool `toml:"advisor"`

	// ConfineToWorkspace restricts the file tools to the directory evilcode was
	// launched in. Off by default: this is a single-user tool on the user's own
	// machine, where refusing to read a file one directory over is friction
	// rather than protection. Turn it on for a session you want kept inside one
	// tree — reviewing an unfamiliar repo, say.
	ConfineToWorkspace bool `toml:"confine_to_workspace"`

	// MaxSteps bounds tool-call rounds in a single turn. Zero — the default —
	// means unbounded: a long refactor converges slowly and legitimately, and a
	// fixed cap cuts off exactly the turns least able to afford losing the
	// work. Set it to reinstate a limit.
	MaxSteps int `toml:"max_steps"`
}

// MCPServer is one `[[mcp]]` block.
type MCPServer struct {
	Name    string   `toml:"name"`
	Command string   `toml:"command"`
	Args    []string `toml:"args"`
	Env     []string `toml:"env"`
}

// DefaultThinkingLines mirrors tui.DefaultThinkingLines. Duplicated rather than
// imported because config must not depend on the UI — the daemon and headless
// both load config and neither draws anything.
const DefaultThinkingLines = 6

// Config is the whole configuration.
type Config struct {
	DefaultModel string            `toml:"default_model"`
	Providers    []ProviderConfig  `toml:"provider"`
	Models       []ModelConfig     `toml:"model"`
	Roles        Roles             `toml:"roles"`
	Display      Display           `toml:"display"`
	Features     Features          `toml:"features"`
	Keybindings  map[string]string `toml:"keybindings"`

	// Dictate is the speech-to-text command `evilcode dictate` runs. A command
	// rather than a bundled engine: STT setups are personal — a local
	// whisper.cpp, a cloud key, a wrapper script — and evilcode has no business
	// having an opinion about which.
	Dictate []string    `toml:"dictate"`
	MCP     []MCPServer `toml:"mcp"`

	// LSP maps a language id to the command that serves it, overriding the
	// built-in defaults (plan.md §17). `lsp.go = ["gopls"]`.
	LSP map[string][]string `toml:"lsp"`

	// Path records where this config was loaded from, or "" for defaults.
	Path string `toml:"-"`
}

// Default returns the configuration used when nothing is on disk: a local
// Ollama, plus Ollama Cloud when a key is in the environment.
func Default() *Config {
	c := &Config{
		DefaultModel: "glm-5.2:cloud@ollama-cloud",
		Providers: []ProviderConfig{
			{Name: "ollama-local", Kind: KindOllama, BaseURL: "http://localhost:11434"},
			{Name: "ollama-cloud", Kind: KindOllama, BaseURL: "https://ollama.com", APIKeyEnv: EnvOllamaKey},
		},
		Display: Display{
			Theme:           "catppuccin-frappe",
			KeybindingHints: true,
			IdleAnimation:   true,
			Overscroll:      "overscroll",
			ThinkingDisplay: "current",
			ThinkingLines:   DefaultThinkingLines,
			InlineDiffs:     true,
		},
		Features: Features{AutoPoke: true, Memory: true},
	}
	// Without a key, ollama.com is unreachable directly — but a local Ollama
	// daemon proxies the same cloud models, so the fallback keeps the model and
	// changes only the route. Falling back to a different model instead meant
	// falling back to one that is usually not even pulled.
	if os.Getenv(EnvOllamaKey) == "" {
		c.DefaultModel = "glm-5.2:cloud@ollama-local"
	}
	return c
}

// ConfigDir is ~/.config/evilcode, honoring XDG_CONFIG_HOME.
func ConfigDir() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "evilcode")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".evilcode"
	}
	return filepath.Join(home, ".config", "evilcode")
}

// DataDir is ~/.local/share/evilcode, honoring XDG_DATA_HOME.
func DataDir() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "evilcode")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".evilcode-data"
	}
	return filepath.Join(home, ".local", "share", "evilcode")
}

// Load reads the config file, applies environment overrides, and validates the
// result. A missing file is not an error — defaults are a working setup.
func Load() (*Config, error) {
	path := os.Getenv(EnvConfigPath)
	if path == "" {
		path = filepath.Join(ConfigDir(), "config.toml")
	}
	return LoadFrom(path)
}

// LoadFrom reads a specific config file.
func LoadFrom(path string) (*Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		// Defaults stand. A missing config file is a working setup, not an error.
	case err != nil:
		return nil, fmt.Errorf("config: reading %s: %w", path, err)
	default:
		// Decoding straight into the defaults means an absent key keeps its
		// default, including booleans that default to true — which a
		// merge-from-zero-value would silently clear.
		if _, err := toml.Decode(string(data), cfg); err != nil {
			return nil, fmt.Errorf("config: parsing %s: %w", path, err)
		}
		cfg.Path = path
	}

	applyEnv(cfg)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// SaveProviderAPIKey updates only the requested provider key while preserving
// unknown TOML text. A full decode/encode round trip would silently delete
// settings newer than this binary knows about.
func SaveProviderAPIKey(providerName, key string) error {
	if providerName == "" {
		return fmt.Errorf("config: provider name is required")
	}
	path := os.Getenv(EnvConfigPath)
	if path == "" {
		path = filepath.Join(ConfigDir(), "config.toml")
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("config: reading %s: %w", path, err)
	}
	updated, found, err := updateProviderKey(string(data), providerName, key)
	if err != nil {
		return fmt.Errorf("config: updating %s: %w", path, err)
	}
	if !found {
		if updated != "" && !strings.HasSuffix(updated, "\n") {
			updated += "\n"
		}
		if updated != "" {
			updated += "\n"
		}
		updated += "[[provider]]\n" +
			"name = " + strconv.Quote(providerName) + "\n" +
			"kind = \"ollama\"\n" +
			"api_key = " + strconv.Quote(key) + "\n"
	}
	return writeConfigAtomic(path, []byte(updated))
}

func updateProviderKey(text, providerName, key string) (string, bool, error) {
	if text == "" {
		return text, false, nil
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	lines := strings.SplitAfter(text, "\n")
	for start := 0; start < len(lines); start++ {
		if strings.TrimSpace(lines[start]) != "[[provider]]" {
			continue
		}
		end := start + 1
		for end < len(lines) {
			trimmed := strings.TrimSpace(lines[end])
			if strings.HasPrefix(trimmed, "[[") || (strings.HasPrefix(trimmed, "[") && trimmed != "") {
				break
			}
			end++
		}
		var header struct {
			Providers []ProviderConfig `toml:"provider"`
		}
		if _, err := toml.Decode(strings.Join(lines[start:end], ""), &header); err != nil {
			return text, false, err
		}
		if len(header.Providers) == 0 || header.Providers[0].Name != providerName {
			start = end - 1
			continue
		}
		value := "api_key = " + strconv.Quote(key) + "\n"
		for i := start + 1; i < end; i++ {
			trimmed := strings.TrimSpace(lines[i])
			name, _, ok := strings.Cut(trimmed, "=")
			if !ok || strings.TrimSpace(name) != "api_key" {
				continue
			}
			indent := lines[i][:len(lines[i])-len(strings.TrimLeft(lines[i], " \t"))]
			lines[i] = indent + value
			return strings.Join(lines, ""), true, nil
		}
		lines = append(lines[:end], append([]string{value}, lines[end:]...)...)
		return strings.Join(lines, ""), true, nil
	}
	return text, false, nil
}

func writeConfigAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config.toml.*")
	if err != nil {
		return fmt.Errorf("creating temporary config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting config permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("syncing config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("installing config: %w", err)
	}
	if dirFile, err := os.Open(dir); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}

func applyEnv(cfg *Config) {
	if m := os.Getenv(EnvModel); m != "" {
		cfg.DefaultModel = m
	}
	if p := os.Getenv(EnvProvider); p == "mock" {
		// The probe rig sets this; a mock provider must exist for it to select.
		if cfg.findProvider("mock") == nil {
			cfg.Providers = append(cfg.Providers, ProviderConfig{Name: "mock", Kind: KindMock})
		}
		cfg.DefaultModel = "mock-large@mock"
	}
}

// Validate reports configuration that cannot work, before anything tries to use
// it. A misconfigured provider should fail at startup with a clear message, not
// mid-turn with a nil dereference.
func (c *Config) Validate() error {
	if len(c.Providers) == 0 {
		return fmt.Errorf("config: no providers configured")
	}
	seen := map[string]bool{}
	for _, p := range c.Providers {
		if p.Name == "" {
			return fmt.Errorf("config: a provider is missing its name")
		}
		if seen[p.Name] {
			return fmt.Errorf("config: duplicate provider name %q", p.Name)
		}
		seen[p.Name] = true
		switch p.Kind {
		case KindOllama, KindOpenAI, KindMock:
		case "":
			return fmt.Errorf("config: provider %q is missing its kind (ollama|openai|mock)", p.Name)
		default:
			return fmt.Errorf("config: provider %q has unknown kind %q", p.Name, p.Kind)
		}
	}
	if c.DefaultModel == "" {
		return fmt.Errorf("config: default_model is not set")
	}
	if _, prov := SplitModelRef(c.DefaultModel); prov != "" && c.findProvider(prov) == nil {
		return fmt.Errorf("config: default_model %q names unknown provider %q", c.DefaultModel, prov)
	}
	return nil
}

// SplitModelRef splits a `model@provider` reference. A bare model name yields
// an empty provider, meaning "the first configured one".
func SplitModelRef(ref string) (model, providerName string) {
	if i := strings.LastIndex(ref, "@"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

func (c *Config) findProvider(name string) *ProviderConfig {
	for i := range c.Providers {
		if c.Providers[i].Name == name {
			return &c.Providers[i]
		}
	}
	return nil
}

// ModelOverrides returns the configured overrides for a model reference, or the
// zero value when none are set.
func (c *Config) ModelOverrides(ref string) ModelConfig {
	model, _ := SplitModelRef(ref)
	for _, m := range c.Models {
		name, _ := SplitModelRef(m.Name)
		if name == model || m.Name == ref {
			return m
		}
	}
	return ModelConfig{}
}

// APIKey resolves a provider's key, preferring the environment.
func (p ProviderConfig) APIKeyValue() string {
	if p.APIKeyEnv != "" {
		if v := os.Getenv(p.APIKeyEnv); v != "" {
			return v
		}
	}
	return p.APIKey
}

// Resolve turns a model reference into a live provider and the bare model name.
// An empty ref uses DefaultModel.
func (c *Config) Resolve(ref string) (provider.Provider, string, error) {
	if ref == "" {
		ref = c.DefaultModel
	}
	model, providerName := SplitModelRef(ref)

	var pc *ProviderConfig
	if providerName == "" {
		pc = &c.Providers[0]
	} else {
		pc = c.findProvider(providerName)
	}
	if pc == nil {
		return nil, "", fmt.Errorf("config: unknown provider %q in model ref %q", providerName, ref)
	}

	p, err := pc.Build()
	if err != nil {
		return nil, "", err
	}
	return p, model, nil
}

// Build constructs the provider client for one configured block.
func (p ProviderConfig) Build() (provider.Provider, error) {
	switch p.Kind {
	case KindOllama:
		return provider.NewOllama(p.Name, p.BaseURL, p.APIKeyValue()), nil
	case KindOpenAI:
		return provider.NewOpenAI(p.Name, p.BaseURL, p.APIKeyValue()), nil
	case KindMock:
		return provider.NewMock(p.Name, ""), nil
	default:
		return nil, fmt.Errorf("config: provider %q has unknown kind %q", p.Name, p.Kind)
	}
}

// RoleChain returns the model references to try for a role, longest-lived
// fallback last. An unset role falls back to the default model, so a config
// that never mentions roles still works.
func (c *Config) RoleChain(role string) []string {
	var chain []string
	switch role {
	case "smol":
		chain = c.Roles.Smol
	case "plan":
		chain = c.Roles.Plan
	case "commit":
		chain = c.Roles.Commit
	default:
		chain = c.Roles.Default
	}
	if len(chain) == 0 {
		return []string{c.DefaultModel}
	}
	return chain
}

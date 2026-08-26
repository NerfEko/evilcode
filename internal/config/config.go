// Package config loads evilcode's TOML configuration and resolves model
// references into live providers (plan.md §16, §1.4).
package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"evilcode/internal/provider"
)

// Env var overrides. Environment beats the config file, and an explicit flag
// beats both.
const (
	EnvModel       = "EVILCODE_MODEL"
	EnvProvider    = "EVILCODE_PROVIDER"
	EnvConfigPath  = "EVILCODE_CONFIG"
	EnvOllamaKey   = "OLLAMA_API_KEY"
	EnvDeepSeekKey = "DEEPSEEK_API_KEY"
	// EnvOllamaSessionCookie feeds the Cloud Usage widget. Ollama exposes no
	// usage API; the widget reads https://ollama.com/settings with the browser's
	// session cookie. A bare value is sent as `__Secure-session=<value>`; a
	// value containing '=' is sent verbatim as a full Cookie header.
	EnvOllamaSessionCookie = "OLLAMA_SESSION_COOKIE"
	// EnvBraveSearchKey is the preferred key name for the optional web_search
	// tool. EnvBraveKey is accepted too because it is a common shorthand.
	EnvBraveSearchKey = "BRAVE_SEARCH_API_KEY"
	EnvBraveKey       = "BRAVE_API_KEY"
)

// Config updates are read-modify-write operations. Serialize them within the
// process so two daemon sessions saving preferences at once cannot each replace
// the file with a view that predates the other's change.
var configWriteMu sync.Mutex

// ProviderKind selects which wire protocol a provider speaks.
type ProviderKind string

const (
	KindOllama   ProviderKind = "ollama"
	KindOpenAI   ProviderKind = "openai"
	KindDeepSeek ProviderKind = "deepseek"
	KindCodex    ProviderKind = "codex"
	KindMock     ProviderKind = "mock"
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

	// AuthFile optionally points at a Codex CLI auth.json. Empty means the
	// standard CODEX_HOME/auth.json (or ~/.codex/auth.json) is discovered.
	// It is ignored by providers that do not use Codex OAuth.
	AuthFile string `toml:"auth_file"`
}

// WebConfig holds credentials for optional web capabilities. It is written
// with restrictive permissions by SaveBraveSearchAPIKey and
// SaveOllamaSessionCookie; environment values still take precedence when
// present.
type WebConfig struct {
	BraveAPIKey string `toml:"brave_api_key"`
	// OllamaSessionCookie is the browser session cookie for the Ollama Cloud
	// usage widget, saved by /connect ollama-usage. The settings page is
	// reachable with the session cookie alone, so it is a live credential and
	// must be guarded like a password.
	OllamaSessionCookie string `toml:"ollama_session_cookie"`
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
	// SkillRetrieval embeds skill summaries alongside memory recall and adds a
	// strong match to the next turn. It is off by default because the extra
	// per-turn embedding call trades prompt-cache stability for discovery.
	SkillRetrieval bool `toml:"skill_retrieval"`

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
	DefaultModel string `toml:"default_model"`

	// LastModel is the model most recently used by the interactive client. It
	// is separate from DefaultModel so an explicit picker default remains a
	// deliberate preference while a normal launch resumes where the user left
	// off.
	LastModel string `toml:"last_model"`

	// ReasoningEfforts remembers the last selected effort by canonical
	// model@provider reference. An effort is a model preference, not a global
	// session setting: switching back to Luna should restore Luna's value.
	ReasoningEfforts map[string]string `toml:"reasoning_efforts"`

	// FavoriteModels are model refs pinned in the picker (plan.md §5.3). They
	// render with a ♥ marker and Shift+Tab cycles the active model through
	// them. Empty until the user pins something.
	FavoriteModels []string          `toml:"favorite_models"`
	Providers      []ProviderConfig  `toml:"provider"`
	Models         []ModelConfig     `toml:"model"`
	Roles          Roles             `toml:"roles"`
	Display        Display           `toml:"display"`
	Features       Features          `toml:"features"`
	Keybindings    map[string]string `toml:"keybindings"`
	Web            WebConfig         `toml:"web"`

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

// The model both default routes point at. Same model either way — only the
// route differs, because a local daemon proxies the same cloud models. Falling
// back to a *different* model instead meant falling back to one that is usually
// not even pulled.
const (
	DefaultCloudModel    = "deepseek-v4-flash:0731@ollama-cloud"
	DefaultLocalModel    = "deepseek-v4-flash:0731@ollama-local"
	DefaultDeepSeekModel = "deepseek-chat"
)

// Default returns the configuration used when nothing is on disk: a local
// Ollama, plus Ollama Cloud when a key is in the environment.
func Default() *Config {
	c := &Config{
		Providers: []ProviderConfig{
			{Name: "ollama-local", Kind: KindOllama, BaseURL: "http://localhost:11434"},
			{Name: "ollama-cloud", Kind: KindOllama, BaseURL: "https://ollama.com", APIKeyEnv: EnvOllamaKey},
			// DeepSeek is its own provider kind, not a bare OpenAI-compatible key:
			// its own base URL, key env var, and model catalogue. It still speaks
			// the OpenAI chat completions API, so Build() serves it with the OpenAI
			// client — only the base URL and key differ.
			{Name: "deepseek", Kind: KindDeepSeek, BaseURL: "https://api.deepseek.com", APIKeyEnv: EnvDeepSeekKey},
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
		// Memory is off by default for now: it fires embedding side-calls and
		// injects recall into every turn, which users should opt into rather than
		// discover after the fact. `memory = true` in the config turns it on.
		Features: Features{AutoPoke: true, Memory: false, SkillRetrieval: false},
	}
	c.DefaultModel = c.preferredDefaultModel()
	// deepseek-v4-flash:0731 is a thinking model, so the default routes pin
	// reasoning effort to high for both the cloud and local routes. A user's
	// saved preference (ReasoningEffortFor) still overrides this at runtime.
	c.ReasoningEfforts = map[string]string{
		DefaultCloudModel: string(provider.ReasoningEffortHigh),
		DefaultLocalModel: string(provider.ReasoningEffortHigh),
	}
	return c
}

// preferredDefaultModel routes to the cloud when a key can actually be resolved
// and to the local daemon when it cannot. Without a key ollama.com is
// unreachable directly, so the local route is the working one.
//
// It reads the *resolved* key rather than the environment, which is the whole
// point: a key saved by /login lives in the config file, and Default() runs
// before that file has been decoded. Deciding from the environment alone left
// every turn routed through a local daemon no matter what you logged in with.
func (c *Config) preferredDefaultModel() string {
	for _, p := range c.Providers {
		if p.Name == "ollama-cloud" && p.APIKeyValue() != "" {
			return DefaultCloudModel
		}
	}
	return DefaultLocalModel
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
		md, err := toml.Decode(string(data), cfg)
		if err != nil {
			return nil, fmt.Errorf("config: parsing %s: %w", path, err)
		}
		cfg.Path = path
		// Default() had to guess the route from the environment, because it
		// builds the very struct this file decodes into. Now that the file's
		// providers — and any key /login wrote into them — are loaded, the guess
		// is re-made. Only when the file does not state a preference of its own.
		if !md.IsDefined("default_model") {
			cfg.DefaultModel = cfg.preferredDefaultModel()
		}
	}

	applyEnv(cfg)
	// Validate normally rejects a default model that names an unlisted provider.
	// Codex is the one intentionally discoverable provider, so make its local
	// auth entry visible before that check when the user selected @codex in the
	// file or through EVILCODE_MODEL. Other paths still discover lazily when a
	// model is explicitly resolved or the picker is opened.
	if _, providerName := SplitModelRef(cfg.DefaultModel); providerName == "codex" {
		cfg.AddDiscoveredCodex()
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// SaveProviderAPIKey updates only the requested provider key while preserving
// unknown TOML text. A full decode/encode round trip would silently delete
// settings newer than this binary knows about.
func SaveProviderAPIKey(providerName, key string) error {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
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
		for _, p := range newProviderSections(updated, providerName, key) {
			updated += "\n" + providerSection(p)
		}
	}
	return writeConfigAtomic(path, []byte(updated))
}

// SaveBraveSearchAPIKey writes the Brave Search key into the dedicated [web]
// table while preserving unknown TOML text. API keys entered through the TUI
// are stored in the user config, which is written with mode 0600.
func SaveBraveSearchAPIKey(key string) error {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("config: Brave Search API key is required")
	}
	path := os.Getenv(EnvConfigPath)
	if path == "" {
		path = filepath.Join(ConfigDir(), "config.toml")
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("config: reading %s: %w", path, err)
	}
	updated, found := writeWebKey(string(data), "brave_api_key", key)
	if !found {
		if updated != "" {
			if !strings.HasSuffix(updated, "\n") {
				updated += "\n"
			}
			updated += "\n"
		}
		updated += "[web]\nbrave_api_key = " + strconv.Quote(key) + "\n"
	}
	return writeConfigAtomic(path, []byte(updated))
}

// SaveOllamaSessionCookie writes the Ollama Cloud session cookie into the
// dedicated [web] table, preserving unknown TOML text. It is entered through
// /connect ollama-usage and stored in the user config, which is written with
// mode 0600.
func SaveOllamaSessionCookie(cookie string) error {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	cookie = strings.TrimSpace(cookie)
	if cookie == "" {
		return fmt.Errorf("config: Ollama session cookie is required")
	}
	path := os.Getenv(EnvConfigPath)
	if path == "" {
		path = filepath.Join(ConfigDir(), "config.toml")
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("config: reading %s: %w", path, err)
	}
	updated, found := writeWebKey(string(data), "ollama_session_cookie", cookie)
	if !found {
		if updated != "" {
			if !strings.HasSuffix(updated, "\n") {
				updated += "\n"
			}
			updated += "\n"
		}
		updated += "[web]\nollama_session_cookie = " + strconv.Quote(cookie) + "\n"
	}
	return writeConfigAtomic(path, []byte(updated))
}

// SaveModelPrefs writes the model picker's preferences — the default model and
// the favorite list — preserving every other line of the file. A full
// decode/encode round trip would silently delete settings newer than this
// binary knows about, so the update is a targeted text edit, exactly like
// SaveProviderAPIKey.
func SaveModelPrefs(defaultModel string, favorites []string) error {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	path := os.Getenv(EnvConfigPath)
	if path == "" {
		path = filepath.Join(ConfigDir(), "config.toml")
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("config: reading %s: %w", path, err)
	}
	updated := updateModelPrefs(string(data), defaultModel, favorites)
	return writeConfigAtomic(path, []byte(updated))
}

// SaveLastModel records the model the interactive client most recently used.
// It intentionally does not rewrite default_model: Ctrl+O remains a deliberate
// default preference, while an ordinary model switch can still be resumed on
// the next launch.
func SaveLastModel(modelRef string) error {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	modelRef = strings.TrimSpace(modelRef)
	if modelRef == "" {
		return fmt.Errorf("config: last model reference is required")
	}
	path := os.Getenv(EnvConfigPath)
	if path == "" {
		path = filepath.Join(ConfigDir(), "config.toml")
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("config: reading %s: %w", path, err)
	}
	updated := updateTopLevelString(string(data), "last_model", modelRef)
	return writeConfigAtomic(path, []byte(updated))
}

// SaveReasoningEffort records one model's effort without dropping preferences
// for other models. The file is read immediately before the update so a long
// lived daemon does not overwrite a newer entry written by another client.
func SaveReasoningEffort(modelRef string, effort provider.ReasoningEffort) error {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	modelRef = strings.TrimSpace(modelRef)
	if modelRef == "" {
		return fmt.Errorf("config: model reference is required for reasoning effort")
	}
	if !effort.Valid() {
		return fmt.Errorf("config: unsupported reasoning effort %q", effort)
	}
	path := os.Getenv(EnvConfigPath)
	if path == "" {
		path = filepath.Join(ConfigDir(), "config.toml")
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("config: reading %s: %w", path, err)
	}

	var saved struct {
		ReasoningEfforts map[string]string `toml:"reasoning_efforts"`
	}
	if len(data) > 0 {
		if _, err := toml.Decode(string(data), &saved); err != nil {
			return fmt.Errorf("config: parsing %s: %w", path, err)
		}
	}
	if saved.ReasoningEfforts == nil {
		saved.ReasoningEfforts = map[string]string{}
	}
	saved.ReasoningEfforts[modelRef] = string(effort)
	updated := updateTopLevelReasoningEfforts(string(data), saved.ReasoningEfforts)
	return writeConfigAtomic(path, []byte(updated))
}

// ReasoningEffortFor returns a saved effort for a canonical model reference.
// Invalid or stale values are ignored so a hand-edited config cannot prevent a
// model from starting; the provider capability list will choose its default.
func (c *Config) ReasoningEffortFor(modelRef string) provider.ReasoningEffort {
	if c == nil {
		return ""
	}
	if raw := c.ReasoningEfforts[strings.TrimSpace(modelRef)]; raw != "" {
		if effort, ok := provider.ParseReasoningEffort(raw); ok {
			return effort
		}
	}
	return ""
}

// updateTopLevelString replaces or inserts one generated top-level TOML
// string. User comments and unknown settings remain byte-for-byte intact.
func updateTopLevelString(text, key, value string) string {
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	lines := strings.SplitAfter(text, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "[") {
			if strings.HasPrefix(trimmed, "[") {
				break
			}
			continue
		}
		name, _, ok := strings.Cut(trimmed, "=")
		if !ok || strings.TrimSpace(name) != key {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = indent + key + " = " + strconv.Quote(value) + "\n"
		return strings.Join(lines, "")
	}

	insert := len(lines) - 1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			insert = i
			break
		}
	}
	entry := key + " = " + strconv.Quote(value) + "\n"
	lines = append(lines[:insert], append([]string{entry}, lines[insert:]...)...)
	return strings.Join(lines, "")
}

// updateTopLevelReasoningEfforts replaces or inserts the compact inline table
// used for the generated per-model effort map. Keys are sorted so repeated
// writes do not churn the config file for map iteration order alone.
func updateTopLevelReasoningEfforts(text string, efforts map[string]string) string {
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	keys := make([]string, 0, len(efforts))
	for key := range efforts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var value strings.Builder
	value.WriteString("reasoning_efforts = {")
	for i, key := range keys {
		if i > 0 {
			value.WriteString(", ")
		}
		value.WriteString(strconv.Quote(key))
		value.WriteString(" = ")
		value.WriteString(strconv.Quote(efforts[key]))
	}
	value.WriteString("}\n")

	lines := strings.SplitAfter(text, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			break
		}
		name, _, ok := strings.Cut(trimmed, "=")
		if !ok || strings.TrimSpace(name) != "reasoning_efforts" {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = indent + value.String()
		return strings.Join(lines, "")
	}

	insert := len(lines) - 1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			insert = i
			break
		}
	}
	lines = append(lines[:insert], append([]string{value.String()}, lines[insert:]...)...)
	return strings.Join(lines, "")
}

// updateModelPrefs rewrites the `default_model` and `favorite_models` keys,
// leaving everything else — comments, unknown keys, provider tables — byte for
// byte alone. Both keys are top-level, so when absent they are inserted before
// the first table header: in TOML a bare key after `[[provider]]` would belong
// to that table and silently never load as the config's own field.
func updateModelPrefs(text, defaultModel string, favorites []string) string {
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	var lead, keep []string
	atTopLevel := true
	for _, line := range strings.SplitAfter(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			if atTopLevel && len(keep) == 0 {
				lead = append(lead, line)
				continue
			}
			keep = append(keep, line)
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			atTopLevel = false
			keep = append(keep, line)
			continue
		}
		name, _, ok := strings.Cut(trimmed, "=")
		if atTopLevel && ok {
			switch strings.TrimSpace(name) {
			case "default_model", "favorite_models":
				continue // replaced by the new values written above the file
			}
		}
		keep = append(keep, line)
	}

	var b strings.Builder
	for _, l := range lead {
		b.WriteString(l)
	}
	b.WriteString("default_model = " + strconv.Quote(defaultModel) + "\n")
	if len(favorites) > 0 {
		b.WriteString("favorite_models = [")
		for i, f := range favorites {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(strconv.Quote(f))
		}
		b.WriteString("]\n")
	}
	for _, l := range keep {
		b.WriteString(l)
	}
	return b.String()
}

// newProviderSections is the provider tables to append when the file has no
// entry for providerName yet.
//
// An explicit `[[provider]]` array *replaces* the defaults at load time rather
// than merging with them, so writing a lone stub into a file that had no
// provider tables leaves a config that will not load at all: Validate rejects
// the default model for naming a provider the file just deleted. When there is
// nothing to replace, the defaults are written out alongside the new key.
func newProviderSections(text, providerName, key string) []ProviderConfig {
	var out []ProviderConfig
	if !hasProviderTable(text) {
		out = append(out, Default().Providers...)
	}
	if providerIndex(out, providerName) < 0 {
		// Seed from the default of the same name so the entry carries its
		// base_url and api_key_env, not just a name and a key.
		seed := ProviderConfig{Name: providerName, Kind: KindOllama}
		if i := providerIndex(Default().Providers, providerName); i >= 0 {
			seed = Default().Providers[i]
		}
		out = append(out, seed)
	}
	if i := providerIndex(out, providerName); i >= 0 {
		out[i].APIKey = key
	}
	return out
}

func providerIndex(providers []ProviderConfig, name string) int {
	for i, p := range providers {
		if p.Name == name {
			return i
		}
	}
	return -1
}

func hasProviderTable(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "[[provider]]" {
			return true
		}
	}
	return false
}

func providerSection(p ProviderConfig) string {
	var b strings.Builder
	b.WriteString("[[provider]]\n")
	b.WriteString("name = " + strconv.Quote(p.Name) + "\n")
	for _, kv := range [][2]string{
		{"kind", string(p.Kind)},
		{"base_url", p.BaseURL},
		{"auth_file", p.AuthFile},
		{"api_key_env", p.APIKeyEnv},
		{"api_key", p.APIKey},
	} {
		if kv[1] != "" {
			b.WriteString(kv[0] + " = " + strconv.Quote(kv[1]) + "\n")
		}
	}
	return b.String()
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

func writeWebKey(text, name, key string) (string, bool) {
	if text == "" {
		return text, false
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	lines := strings.SplitAfter(text, "\n")
	for start := 0; start < len(lines); start++ {
		if strings.TrimSpace(lines[start]) != "[web]" {
			continue
		}
		end := start + 1
		for end < len(lines) {
			trimmed := strings.TrimSpace(lines[end])
			if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
				break
			}
			end++
		}
		value := name + " = " + strconv.Quote(key) + "\n"
		for i := start + 1; i < end; i++ {
			trimmed := strings.TrimSpace(lines[i])
			field, _, ok := strings.Cut(trimmed, "=")
			if !ok || strings.TrimSpace(field) != name {
				continue
			}
			indent := lines[i][:len(lines[i])-len(strings.TrimLeft(lines[i], " \t"))]
			lines[i] = indent + value
			return strings.Join(lines, ""), true
		}
		lines = append(lines[:end], append([]string{value}, lines[end:]...)...)
		return strings.Join(lines, ""), true
	}
	return text, false
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
		// The mock model stands in for a frontier one, vision included: without
		// this `read` on an image reports "this model cannot see images" and the
		// probe rig could never capture the inline-image frame.
		if cfg.ModelOverrides(cfg.DefaultModel).Name == "" {
			cfg.Models = append(cfg.Models, ModelConfig{Name: "mock-large@mock", Vision: true})
		}
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
		case KindOllama, KindOpenAI, KindDeepSeek, KindMock:
		case KindCodex:
			if p.APIKey != "" || p.APIKeyEnv != "" {
				return fmt.Errorf("config: codex provider %q uses Codex OAuth, not api_key/api_key_env", p.Name)
			}
		case "":
			return fmt.Errorf("config: provider %q is missing its kind (ollama|openai|deepseek|codex|mock)", p.Name)
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

// ModelRef builds a `model@provider` reference from its parts, the inverse of
// SplitModelRef. A bare model name (no provider) round-trips unchanged. Used
// to record the model a session is on so a later resume resolves back to the
// same provider.
func ModelRef(model, provider string) string {
	if provider == "" {
		return model
	}
	return model + "@" + provider
}

func (c *Config) findProvider(name string) *ProviderConfig {
	for i := range c.Providers {
		if c.Providers[i].Name == name {
			return &c.Providers[i]
		}
	}
	return nil
}

// FindProvider is the exported lookup for callers outside config, e.g. the
// /login command validating a provider name before accepting a key.
func (c *Config) FindProvider(name string) *ProviderConfig {
	return c.findProvider(name)
}

// ModelOverrides returns the configured overrides for a model reference, or the
// zero value when none are set.
func (c *Config) ModelOverrides(ref string) ModelConfig {
	model, _ := SplitModelRef(ref)
	// An exact provider-qualified override wins regardless of declaration order.
	// A qualified override for foo@one must never leak onto foo@two merely because
	// both references share the same bare model name.
	for _, m := range c.Models {
		if m.Name == ref {
			return m
		}
	}
	for _, m := range c.Models {
		name, providerName := SplitModelRef(m.Name)
		if providerName == "" && name == model {
			return m
		}
	}
	return ModelConfig{}
}

// ContextWindowDiscovery bounds the one metadata request made at startup. It is
// a nicety — the meter falls back to a guess — so it must never be what makes
// launching slow.
const ContextWindowDiscovery = 3 * time.Second

// ContextWindowFor resolves a model's context window. An explicit
// `[[model]] context_window` wins; otherwise the provider is asked, which is
// what saves hand-writing a block per model for a catalogue of seventeen. Zero
// means nobody knew and the caller's own default stands.
//
// Only Ollama can answer today, so this type-asserts rather than widening the
// Provider interface with a method the other two would have to stub.
func ContextWindowFor(prov provider.Provider, model string, override int) int {
	if override > 0 {
		return override
	}
	o, ok := prov.(*provider.Ollama)
	if !ok {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), ContextWindowDiscovery)
	defer cancel()
	info, err := o.Show(ctx, model)
	if err != nil {
		return 0
	}
	return info.ContextWindow
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

// BraveSearchAPIKey returns a Brave key supplied through the environment.
// Config-file values are resolved by (*Config).BraveSearchAPIKey so callers
// that already loaded configuration do not need to read it a second time.
func BraveSearchAPIKey() string {
	for _, name := range []string{EnvBraveSearchKey, EnvBraveKey} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// BraveSearchAPIKey resolves the Brave key with environment precedence over
// the value saved by /connect brave.
func (c *Config) BraveSearchAPIKey() string {
	if value := BraveSearchAPIKey(); value != "" {
		return value
	}
	return strings.TrimSpace(c.Web.BraveAPIKey)
}

// OllamaSessionCookie returns the Ollama Cloud session cookie supplied through
// the environment. Config-file values are resolved by
// (*Config).OllamaSessionCookie so callers that already loaded configuration
// do not need to read it a second time.
func OllamaSessionCookie() string {
	return strings.TrimSpace(os.Getenv(EnvOllamaSessionCookie))
}

// OllamaSessionCookie resolves the cookie with environment precedence over the
// value saved by /connect ollama-usage.
func (c *Config) OllamaSessionCookie() string {
	if value := OllamaSessionCookie(); value != "" {
		return value
	}
	return strings.TrimSpace(c.Web.OllamaSessionCookie)
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
		// Codex is intentionally discovered lazily. Keeping it out of Default()
		// preserves a deterministic, offline config while an installed Codex
		// account remains selectable with `@codex` and in the picker.
		if pc == nil && providerName == "codex" {
			c.AddDiscoveredCodex()
			pc = c.findProvider(providerName)
		}
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
	case KindDeepSeek:
		// DeepSeek speaks the OpenAI-compatible chat completions API, so the
		// OpenAI client supplies the transport — its thinking wrapper is selected
		// explicitly because DeepSeek's reasoning_effort vocabulary differs.
		return provider.NewOpenAI(p.Name, p.BaseURL, p.APIKeyValue()).
			WithDeepSeekReasoning(), nil
	case KindCodex:
		auth, err := provider.DiscoverCodexAuthAt(p.AuthFile)
		if err != nil {
			return nil, fmt.Errorf("config: codex provider %q: %w", p.Name, err)
		}
		return provider.NewCodex(p.Name, p.BaseURL, auth), nil
	case KindMock:
		return provider.NewMock(p.Name, ""), nil
	default:
		return nil, fmt.Errorf("config: provider %q has unknown kind %q", p.Name, p.Kind)
	}
}

// AddDiscoveredCodex adds a ready-to-build Codex provider when the Codex CLI's
// ChatGPT OAuth account is present. It is safe to call repeatedly and never
// changes the default model or performs network I/O.
func (c *Config) AddDiscoveredCodex() bool {
	if c == nil || c.findProvider("codex") != nil {
		return c != nil && c.findProvider("codex") != nil
	}
	auth, err := provider.DiscoverCodexAuth()
	if err != nil {
		return false
	}
	c.Providers = append(c.Providers, ProviderConfig{
		Name:     "codex",
		Kind:     KindCodex,
		BaseURL:  provider.DefaultCodexBaseURL,
		AuthFile: auth.AuthFile,
	})
	return true
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

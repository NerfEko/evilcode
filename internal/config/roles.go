package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"evilcode/internal/provider"
)

// Role names for model routing (plan.md §16).
const (
	RoleDefault = "default"
	RoleSmol    = "smol"
	RolePlan    = "plan"
	RoleCommit  = "commit"
)

// RepoConfigName is the per-repo override file. A repository can pin which
// models its ambient work uses without touching the user's global config.
const RepoConfigName = ".evilcode.toml"

// LoadRepoOverrides merges a repo-root `.evilcode.toml` over the config. Only
// the blocks a repo has any business pinning are honored.
func (c *Config) LoadRepoOverrides(repoRoot string) error {
	if repoRoot == "" {
		return nil
	}
	path := filepath.Join(repoRoot, RepoConfigName)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}

	// Deliberately narrow: a repo may pin roles and the default model, but not
	// provider credentials. Checking out a repository must not be able to
	// redirect the user's API keys somewhere new.
	var repo struct {
		DefaultModel string        `toml:"default_model"`
		Roles        Roles         `toml:"roles"`
		Models       []ModelConfig `toml:"model"`
	}
	if _, err := toml.Decode(string(data), &repo); err != nil {
		return fmt.Errorf("config: parsing %s: %w", path, err)
	}

	if repo.DefaultModel != "" {
		c.DefaultModel = repo.DefaultModel
	}
	if repo.Roles.Default != nil {
		c.Roles.Default = repo.Roles.Default
	}
	if repo.Roles.Smol != nil {
		c.Roles.Smol = repo.Roles.Smol
	}
	if repo.Roles.Plan != nil {
		c.Roles.Plan = repo.Roles.Plan
	}
	if repo.Roles.Commit != nil {
		c.Roles.Commit = repo.Roles.Commit
	}
	// Model blocks merge by reference with last-wins precedence. Appending used
	// to be the whole story, and ModelOverrides returns the first match — so a
	// global block for the same model silently beat the repo's pin, the exact
	// opposite of what "overrides" says. A repo entry with the same name
	// replaces the global one; new names append. The resolution rule — a
	// provider-qualified entry beats a bare one for that provider — lives in
	// ModelOverrides and is untouched (R2-09).
	for _, m := range repo.Models {
		replaced := false
		for i := range c.Models {
			if c.Models[i].Name == m.Name {
				c.Models[i] = m
				replaced = true
				break
			}
		}
		if !replaced {
			c.Models = append(c.Models, m)
		}
	}
	return nil
}

// Clone returns a copy safe to apply repo overrides to.
//
// The daemon holds one config for every session it hosts, and overrides used to
// be applied by mutating it: one repository's pinned model became every
// session's pinned model, and two builds racing rewrote it under each other.
//
// Deep where overrides write. Slices are copied because LoadRepoOverrides
// appends to Models, and the role pointers are shared because it replaces them
// rather than writing through them.
func (c *Config) Clone() *Config {
	if c == nil {
		return nil
	}
	out := *c
	out.Providers = append([]ProviderConfig(nil), c.Providers...)
	out.Models = append([]ModelConfig(nil), c.Models...)
	out.FavoriteModels = append([]string(nil), c.FavoriteModels...)
	out.Roles.Default = append([]string(nil), c.Roles.Default...)
	out.Roles.Smol = append([]string(nil), c.Roles.Smol...)
	out.Roles.Plan = append([]string(nil), c.Roles.Plan...)
	out.Roles.Commit = append([]string(nil), c.Roles.Commit...)
	if c.ReasoningEfforts != nil {
		out.ReasoningEfforts = make(map[string]string, len(c.ReasoningEfforts))
		for k, v := range c.ReasoningEfforts {
			out.ReasoningEfforts[k] = v
		}
	}
	out.MCP = append([]MCPServer(nil), c.MCP...)
	for i := range out.MCP {
		out.MCP[i].Args = append([]string(nil), c.MCP[i].Args...)
		out.MCP[i].Env = append([]string(nil), c.MCP[i].Env...)
	}
	out.Dictate = append([]string(nil), c.Dictate...)
	if c.Keybindings != nil {
		out.Keybindings = make(map[string]string, len(c.Keybindings))
		for k, v := range c.Keybindings {
			out.Keybindings[k] = v
		}
	}
	if c.LSP != nil {
		out.LSP = make(map[string][]string, len(c.LSP))
		for k, v := range c.LSP {
			out.LSP[k] = append([]string(nil), v...)
		}
	}
	return &out
}

// Router resolves a role to a working provider, trying the role's fallback
// chain in order.
type Router struct {
	cfg *Config
}

// NewRouter builds a router over a config.
func (c *Config) Router() *Router { return &Router{cfg: c} }

// Resolved is one role's resolved backend.
type Resolved struct {
	Provider provider.Provider
	Model    string
	Ref      string
}

// For resolves a role. It walks the fallback chain and returns the first entry
// that builds, so a chain naming an unconfigured provider degrades to the next
// rather than failing the call.
//
// Every internal side-call — memory extraction, session titles, digests, commit
// messages — goes through the `smol` role, so ambient work never silently burns
// the main model (plan.md §16).
func (r *Router) For(role string) (Resolved, error) {
	chain := r.cfg.RoleChain(role)
	var firstErr error
	for _, ref := range chain {
		p, model, err := r.cfg.Resolve(ref)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		return Resolved{Provider: p, Model: model, Ref: ref}, nil
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("config: role %q has no usable model", role)
	}
	return Resolved{}, firstErr
}

// SideCall runs a cheap one-shot completion through a role, with no tools and
// no session. It is the plumbing every ambient feature uses.
//
// The fallback chain is a runtime fallback, not just a build-time one (R2-10):
// an authentication failure, a rate limit, a model-not-found, a timeout, or a
// mid-stream error all move to the next configured entry instead of failing a
// call that had alternatives. The shared context bounds the whole chain — a
// spent deadline stops the walk, and the diagnostics name every entry that was
// tried without exposing anything a credential could be reconstructed from.
func (r *Router) SideCall(ctx context.Context, role, system, user string) (string, error) {
	chain := r.cfg.RoleChain(role)
	var errs []string
	for _, ref := range chain {
		p, model, err := r.cfg.Resolve(ref)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", ref, err))
			continue
		}
		out, err := sideCallOnce(ctx, p, model, system, user)
		if err == nil {
			return out, nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v", ref, err))
		if ctx.Err() != nil {
			// The shared context or deadline is spent; no entry can be
			// attempted within the caller's budget.
			break
		}
	}
	if len(errs) == 0 {
		return "", fmt.Errorf("config: role %q has no usable model", role)
	}
	return "", fmt.Errorf("role %q failed: %s", role, strings.Join(errs, "; "))
}

func sideCallOnce(ctx context.Context, p provider.Provider, model, system, user string) (string, error) {
	var msgs []provider.Message
	if system != "" {
		msgs = append(msgs, provider.Message{Role: provider.RoleSystem, Content: system})
	}
	msgs = append(msgs, provider.Message{Role: provider.RoleUser, Content: user})

	ch, err := p.ChatStream(ctx, provider.Req{Model: model, Messages: msgs})
	if err != nil {
		return "", err
	}

	var b strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			return "", chunk.Err
		}
		b.WriteString(chunk.Text)
	}
	return strings.TrimSpace(b.String()), nil
}

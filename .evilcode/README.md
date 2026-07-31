# .evilcode

Per-repo evilcode configuration.

- `skills/` — instruction files loaded by name into the system prompt. Bodies
  load only when the model calls the `skill` tool, which keeps the prompt
  cacheable as the set grows. Either `name.md` or `name/SKILL.md` works, and the
  description comes from frontmatter when present.
- `.evilcode.toml` at the repo root may pin `[roles]` and `default_model`. It
  deliberately cannot add providers: checking out a repository must not be able
  to point your API keys somewhere new.

A repo's skills shadow the user's in `~/.config/evilcode/skills/`.

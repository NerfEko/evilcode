---
description: how skills work in this repo
---

# Skills

Files here are loaded by name and one-line description into the system prompt.
Bodies load only when the model calls the `skill` tool, which is what keeps the
prompt cacheable as the set grows.

Either layout works: `name.md`, or `name/SKILL.md`. The description comes from
frontmatter when present, otherwise the first non-heading line.

A repo's skills shadow the user's `~/.config/evilcode/skills/`, so a project can
ship instructions without anyone installing anything.

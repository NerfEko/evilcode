// evilcode web entry point. Phase 1 ships the shell only: the module graph,
// SSE client, mirror reducer, and views arrive in Phases 2–6. This module
// exists so the asset pipeline, CSP (`script-src 'self'`), and embedding are
// proven end to end before any behavior is built on them.

console.debug("evilcode web shell loaded");
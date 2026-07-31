# LOOPS.md — flight recorder

Append-only. Never edit old entries. One entry per task from `plan.md`.

## 2026-07-30 P0.1 — repo bootstrap

Done: `git init` (branch `main`), `go mod init evilcode` (go 1.26.4), `.gitignore`
(binary, `probe/frames/`, `shots/`, `*.png` with a `testdata` exception).

Skeleton dirs deliberately not created empty — git does not track empty directories, so
packages are created as their first file lands. Not a spec deviation, just how git works.

Verified: `go build ./...` (no packages yet, trivially green).

## 2026-07-30 P0.2 — seed process files

Done: `LOOPS.md` (this file), `README.md` (§0.2.8 sections: what it is, build, quickstart,
what works today, config reference, keymap, probe rig), `DEVIATIONS.md`.

Verified: files exist, README describes only what actually ships.

Environment note for future loops: the build sandbox mounts `$GOPATH/pkg/mod` read-only
and blocks network, so `go` commands that touch the module cache must run unsandboxed.

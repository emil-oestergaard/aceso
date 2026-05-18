---
description: Commit, push, and open a PR
argument-hint: [optional intent/notes for the commit & PR]
allowed-tools: Bash(git:*), Bash(gh:*), Bash(go:*), Bash(gofmt:*)
---

Commit the current changes, push the branch, and open a pull request.

Optional context from the user: $ARGUMENTS

Follow this sequence exactly. Stop and report if any step fails — do not skip ahead.

## 1. Assess

- Run `git status`, `git diff`, and `git diff --staged` to see every change.
- If there is nothing to commit, stop and report "nothing to commit".
- Get the current branch: `git rev-parse --abbrev-ref HEAD`.
- If on `main`, **do not commit to it.** Create a branch first — pick a short
  kebab-case `<type>/<slug>` name from the change and `git checkout -b` it.

## 2. Verify — CI gates (must pass before commit)

Run from `agent/`:

- `go build ./...`
- `go vet ./...`
- `go test -race -cover -count=1 ./...`
- `gofmt -l .` — must print nothing

If any gate fails, **STOP. Do not commit.** Report the failure and what to fix.

## 3. Commit

- Stage the relevant files.
- Write a Conventional Commits message — `<type>: <description>`, where type is
  one of `feat|fix|refactor|docs|test|chore|perf|ci`. Base the message on the
  whole diff, not a single file.
- Do **not** add an AI attribution or `Co-Authored-By` trailer (disabled
  globally).
- Before committing, confirm repo rules: docs land with code (rule 1),
  `docs/status.md` is flipped if a capability moved stub/wired/deferred or
  tests were added/removed (rule 2), and tests land with code (rule 4). If the
  diff violates these, fix it first or report.

## 4. Push

- `git push -u origin <branch>`.
- If git reports the branch up-to-date but the remote branch does not actually
  exist (stale upstream tracking ref), run `git fetch --prune`, then push
  explicitly with `git push -u origin <branch>`.

## 5. Open PR

- Base branch: `main`.
- Create the PR with `gh pr create`:
  - Title matches the commit intent.
  - Body covers: a summary of all changes (use `git diff main...HEAD`, not just
    the latest commit), the rationale, and a test plan.
- Report the PR URL.

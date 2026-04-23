#!/usr/bin/env bash
#
# Git pre-commit hook: gofmt (staged files only) + go vet + golangci-lint.
#
# Installed by `make hooks-install`, which symlinks this file to
# .git/hooks/pre-commit. Bypass with `git commit --no-verify` if you
# really need to (the hook is a convenience, not a gate — CI is the
# real gate).
#
# Known limitation: checks run against the working tree, not a pure
# index view, so unstaged edits in files that are also staged are
# visible to the linters. Stash local changes if you want a pure
# pre-commit check.

set -eu

ROOT=$(git rev-parse --show-toplevel)
cd "$ROOT"

# Collect staged Go files (added, copied, modified, renamed) as a
# newline-separated string. Portable across bash 3.2 (macOS default)
# and bash 4+ — no `mapfile`.
STAGED_GO=$(git diff --cached --name-only --diff-filter=ACMR -- '*.go' 2>/dev/null || true)

if [ -z "$STAGED_GO" ]; then
  # No Go changes — skip. CI still runs the same checks on every PR
  # so rare drift (go.mod without staged .go, etc.) is covered there.
  exit 0
fi

bold() { printf '\033[1m%s\033[0m\n' "$*"; }
red()  { printf '\033[31m%s\033[0m\n' "$*"; }

# 1. gofmt: scope to staged files. Fails with a clear one-liner
#    telling the user exactly how to fix it.
unformatted=$(echo "$STAGED_GO" | xargs gofmt -l 2>/dev/null || true)
if [ -n "$unformatted" ]; then
  red "pre-commit: gofmt needed:"
  printf '  %s\n' $unformatted
  echo
  one_line=$(echo "$unformatted" | tr '\n' ' ')
  echo "  fix:  gofmt -w $one_line"
  echo "  then: git add $one_line"
  exit 1
fi

# 2. go vet: repo-wide, with the e2e build tag so //go:build e2e
#    files get the same scrutiny. Fast, and catches cross-file issues
#    a file-scoped pass would miss. If this ever gets slow, scope to
#    changed packages via `go list`.
bold "pre-commit: go vet -tags=e2e ./..."
if ! go vet -tags=e2e ./...; then
  exit 1
fi

# 3. golangci-lint: prefer the pinned binary installed by
#    `make golangci-lint`, fall back to PATH.
LINT_BIN="$ROOT/bin/golangci-lint"
if [ ! -x "$LINT_BIN" ]; then
  LINT_BIN=$(command -v golangci-lint 2>/dev/null || true)
fi
if [ -z "$LINT_BIN" ]; then
  red "pre-commit: golangci-lint not found"
  echo "  run: make golangci-lint    # installs the pinned version under ./bin"
  exit 1
fi
bold "pre-commit: $(basename "$LINT_BIN") run"
if ! "$LINT_BIN" run; then
  exit 1
fi

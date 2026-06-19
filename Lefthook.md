# Lefthook

Lefthook is a fast, dependency-free Git hooks manager written in Go. It replaces shell scripts in `.git/hooks/` with a single YAML file (`lefthook.yaml`) committed to the repository and shared across the team.

## Installation

```sh
# macOS
brew install lefthook

# Go toolchain
go install github.com/evilmartians/lefthook@latest
```

Verify the version (this project requires ≥ 2.0.0):

```sh
lefthook --version
```

## Activating Hooks

After cloning, install the Git hooks once:

```sh
lefthook install
```

This writes thin shim scripts into `.git/hooks/` that delegate to lefthook. It only needs to be run once per clone. Other developers pick up changes to `lefthook.yaml` automatically on `git pull` — no reinstall required.

To remove the hooks:

```sh
lefthook uninstall
```

## Running Hooks Manually

```sh
# Run all pre-commit jobs
lefthook run pre-commit

# Run all pre-push jobs
lefthook run pre-push

# Run a single named job
lefthook run pre-commit --jobs fmt-roster

# Dry-run: show what would run without executing
lefthook run pre-commit --dry-run

# Force all jobs to run even if no matching files are staged
lefthook run pre-commit --all-files
```

## Skipping Hooks

```sh
# Skip all hooks for one commit
LEFTHOOK=0 git commit -m "wip"

# Skip all hooks for one push
LEFTHOOK=0 git push

# Exclude specific jobs by name
LEFTHOOK_EXCLUDE=fmt-roster,fmt-teller git commit -m "wip"
```

______________________________________________________________________

## `pre-commit` Hook

On every `git commit`, for each Go module that has **staged** `.go` files, lefthook runs:

```sh
golangci-lint fmt --config=../.golangci.yml
```

All 19 modules run in parallel. Only modules with staged `.go` files are processed — the `glob: "*.go"` field filters each job against the staged file list.

### What `golangci-lint fmt` Actually Does

`golangci-lint fmt` applies the three formatters enabled in `.golangci.yml` in a single pass:

| Formatter | What it enforces                                                           |
| --------- | -------------------------------------------------------------------------- |
| `gofumpt` | Stricter superset of `gofmt` — extra blank-line rules, consistent grouping |
| `gci`     | Import block ordering: stdlib → third-party → `github.com/Khan/…`          |
| `golines` | Wraps lines exceeding 100 characters; shortens long comments               |

Running all three here ensures the commit matches exactly what `golangci-lint run` will enforce at push time. Using `gofmt` alone would cause a push failure if `gofumpt` or `gci` found violations.

### Why `stage_fixed: true`

After formatting, lefthook automatically re-stages the modified files so they are included in the commit. Without this, you would commit the unformatted version and the formatted version would be left as unstaged changes.

______________________________________________________________________

## `pre-push` Hook

On every `git push`, for each Go module that has `.go` files **in the commits being pushed**, lefthook runs four commands sequentially inside that module's directory:

```sh
go mod tidy -diff                                       # 1
go vet ./...                                            # 2
go test -count=1 -timeout 5m ./...                      # 3
golangci-lint run --fix --config=../.golangci.yml ./... # 4
```

All 19 modules run in parallel with each other, so total wall-clock time is bounded by the slowest module, not the sum.

The `glob: "*.go"` field here matches against files changed in the pushed commits (not the staging area), so modules with no changes are skipped entirely.

### Why Each Step, in This Order

**`go mod tidy -diff`** — Exits non-zero if `go.mod` or `go.sum` would change under `go mod tidy`, without modifying files. Running this first prevents pushing a module with missing or stale dependency declarations, which would break other developers' builds immediately.

**`go vet ./...`** — The Go toolchain's built-in static analyser. The config enables the full set of vet analyzers (appends, atomic, copylocks, loopclosure, lostcancel, printf, unreachable, and more). Fast and zero false-positives, so it runs before the slower test suite.

**`go test -count=1 -timeout 5m ./...`** — Full test suite with `-count=1` to bypass the test cache. Every push exercises real results rather than a potentially stale cached run. The 5-minute timeout prevents a hung test from blocking a push indefinitely.

**`golangci-lint run --fix --config=../.golangci.yml ./...`** — Runs the full linter suite (26 linters) and applies auto-fixes where available. This runs last because linters can produce false positives on code that does not compile or pass tests; running it after confirms the code is at least correct before enforcing style.

### Why `piped: true` Within Each Module

The four steps must run in this order because each depends on the previous succeeding:

- No point running tests if `go mod tidy` reveals the module is inconsistent.
- No point linting if tests fail — the code may be structurally broken.

`piped: true` aborts the chain at the first failure and immediately reports which step failed.

### Recovery When `pre-push` Fails

If `golangci-lint run --fix` modifies files, the push is still rejected (the fixed files are not part of the pushed commits). The recovery workflow is:

```sh
# 1. Stage the fixes golangci-lint applied
git add -p # or: git add <specific files>

# 2. Amend the last commit (or create a new one)
git commit --amend --no-edit

# 3. Push again — hooks run on the amended commit
git push
```

If `go mod tidy` fails, the fix is:

```sh
go mod tidy # run inside the affected module's directory
git add go.mod go.sum
git commit --amend --no-edit
git push
```

______________________________________________________________________

## Modules

The following 19 Go modules are covered by both hooks. Each has its own `go.mod` — this is a Go workspace repo (`go.work`). The shared linter config at `.golangci.yml` in the repo root is referenced from each module job via `--config=../.golangci.yml`.

`admin-reports`, `alerter`, `distutil`, `instructional-area-gen`, `isyncso`, `khanx`, `khanxadmin`, `listdistricts`, `lms-connect`, `lockwatch`, `pkg`, `pull-demographics`, `pull-test-results`, `repoman`, `roster`, `rosterjob-updates`, `signer`, `teller`, `yearend`

______________________________________________________________________

## Adding a New Module

1. Create the module directory and initialize it:

   ```sh
   mkdir newmodule
   cd newmodule
   go mod init github.com/Khan/districts-ff/newmodule
   ```

2. Add it to the workspace:

   ```sh
   go work use ./newmodule
   ```

3. Add the following two blocks to `lefthook.yaml`, replacing `newmodule` with the actual directory name:

   **Under `pre-commit → jobs`:**

   ```yaml
     - name: fmt-newmodule
       root: newmodule/
       glob: '*.go'
       run: golangci-lint fmt --config=../.golangci.yml
       stage_fixed: true
   ```

   **Under `pre-push → jobs`:**

   ```yaml
     - name: newmodule
       root: newmodule/
       glob: '*.go'
       group:
         piped: true
         jobs:
           - run: go mod tidy -diff
           - run: go vet ./...
           - run: go test -count=1 -timeout 5m ./...
           - run: golangci-lint run --fix --config=../.golangci.yml ./...
   ```

4. Commit `lefthook.yaml` and `go.work` together.

______________________________________________________________________

## Troubleshooting

**Hook didn't fire on commit/push**
Run `lefthook install` — the shim scripts in `.git/hooks/` may be missing or stale.

**`golangci-lint: command not found`**
Install golangci-lint: `brew install golangci-lint` or `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest`.

**`go mod tidy -diff` fails with "flag provided but not defined"**
`-diff` requires Go 1.23 or later. Run `go version` and update if needed.

**A module's jobs are not running even though files changed**
Check that the staged (pre-commit) or pushed (pre-push) files are `.go` files inside that module's directory. The `glob: "*.go"` filter is scoped to the `root:` directory.

**Want to run hooks without triggering CI / without a real push**
Use `lefthook run pre-push --all-files` to run all pre-push jobs locally against all `.go` files, regardless of what is being pushed.

# Khan Academy Districts

Khan Academy districts integration services — 19-module Go workspace (`go.work`).

## Skills

Detailed how-to guides live in `.agents/skills/`. Read the relevant `SKILL.md` before
starting the task it covers. The table below maps tasks to skills.

| Task                                                        | Skill                                   |
| ----------------------------------------------------------- | --------------------------------------- |
| Run or write Go tests; coverage; benchmarks; suite patterns | `.agents/skills/go-test-tools/SKILL.md` |
| Find and run only affected tests after code changes (gta)   | `.agents/skills/go-test-auto/SKILL.md`  |
| Run Go linters; fix lint errors; golangci-lint; repowrite   | `.agents/skills/lintme/SKILL.md`        |
| Lint or fix markdown prose; vale errors; vocabulary files   | `.agents/skills/vale/SKILL.md`          |
| Create or manage stacked GitHub PRs                         | `.agents/skills/gh-stack/SKILL.md`      |
| GitHub CLI: create PRs, check status, manage issues         | `.agents/skills/gh-cli/SKILL.md`        |
| Create a git commit with correct format                     | `.agents/skills/git-commit/SKILL.md`    |
| Write SQL queries; regenerate sqlc code; use Querier        | `.agents/skills/sqlc/SKILL.md`          |
| Create or configure PostgreSQL connection pools; pgxpool    | `.agents/skills/pgxpool/SKILL.md`       |

## Go Guidelines

`RULES.md` at the repo root contains Go code guidelines. Apply them when writing or
reviewing Go code in any module.

## Repo Conventions

**Workspace:** 19 modules share `go.work`. Tests run per-module from within each module
directory; the workspace root is not a runnable module. The `lintme` skill handles running
linters across all modules.

**Formatting:** `golangci-lint fmt --config=../.golangci.yml` (lefthook runs this automatically
on pre-commit for changed files).

**Datastore writes:** Direct puts/deletes on `UserDistrictInfo`, `ClassroomDistrictInfo`,
`District`, and `School` are prohibited. Use `repo.*` functions from `pkg/repo/`. The
`repowrite` analyzer enforces this at compile time — see the `lintme` skill.

**Test suites:** Tests use testify suites via `khantest.Suite` (pure logic, no external
services) or `servicetest.Suite` (with Cloud Datastore emulator). Do not write tests
using the hand-rolled `assert`/`equals` helper pattern from `RULES.md` — follow the
suite pattern documented in the `go-test-tools` skill instead.

**SQL:** Uses `pgx` and `sqlc` for PostgreSQL (as permitted by RULES.md §8). The transaction
and query patterns in RULES.md apply — use `pgx` transaction types rather than `*sql.Tx`.

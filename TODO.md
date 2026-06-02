# Deferred work

Items the linter has flagged that the team has decided not to fix immediately.
The `.golangci.yaml` standard is not relaxed; instead these are addressed
opportunistically — when you touch the surrounding code for another reason,
fix the relevant items here.

For a clean view of what the linter still complains about, run
`golangci-lint run --config=./.golangci.yaml ./...`. To see only what *new*
work introduces relative to main, run
`golangci-lint run --new-from-rev=origin/main`.

## Possible real bugs

These three are worth investigating the next time the surrounding code is edited.
They are not stylistic — the lint flags them because the analyzer suspects an
actual defect.

- **`gosec G602` — slice-bounds risk in `execengine/execengine.go:735,764,769`**
  (mirror sites in `generated/defederator/federation_exec.go`).
  `mergeEntityResults` indexes `path[0]` / `path[1:]` after a `len(path) == 0`
  early return, but the analyzer thinks at least one branch can reach the index
  with `len(path) == 0`. Verify the invariant holds for every code path, or add
  a defensive check.

- **`musttag` — unmarshaling into untagged structs**
  at `execengine/execengine.go:160,203` and the mirror in
  `generated/defederator/federation_exec.go:160,203`.
  `json.Unmarshal(specJSON, &raw)` decodes into a struct whose fields lack
  `json:"..."` tags. Decoding then relies on case-insensitive Go field-name
  matching, which is silent and easy to break with a rename. Either add explicit
  tags or document why the bare names are correct.

- **`gocritic: dupArg` — no-op `ReplaceAll` in `migrate/migrate_test.go:72`**:
  `strings.ReplaceAll(fixtureGenqlientYAML, "../../schema/", "../../schema/")`
  replaces a substring with itself. The test passes today because the
  replacement is a no-op, which means the substitution this line was *meant*
  to perform is missing.

## Architectural — defer until a focused PR

These are real code-quality issues, but each touches many files and warrants a
deliberate effort rather than a side errand.

| Category | Sites | Notes |
| --- | --- | --- |
| `wrapcheck` | 18 | Wrap every external error with `fmt.Errorf("…: %w", err)`. Touches nearly every error return that crosses a package boundary. |
| `testpackage` | 17 | Rename remaining `package generator` / `package migrate` test files to `_test`. Each renamed file must switch to using the public API; some internal-only tests may need a small refactor. |
| `revive: cognitive-complexity` | ~28 | Function decomposition in `execengine`, `generator`, `entitymerge`. Many of these are template/code-emission functions that resist decomposition; some are truly tangled and worth splitting. |
| `gocritic` (rangeValCopy, hugeParam, ifElseChain) | 21 | Performance-relevant signature changes — switch to pointer receivers / pointer ranges. Benchmark first; the structs in question (`urlSpecEntityFetch` at 168 bytes, etc.) are on hot paths. |
| `nestif` | 9 | Same cognitive-complexity cluster. Often resolves naturally when the surrounding function is decomposed. |
| `gochecknoglobals` | 5 | Package-level maps used as constants (`keepBindingType`, `federationDirectiveNames`, etc.). Either inline them, hoist into functions returning the map, or accept a `//nolint:gochecknoglobals` after team review. |
| `cyclop` | 3 | Cyclomatic complexity on `applyProjection`, `collectLeavesRaw`, `mergeEntityResults`. Likely splits naturally with the cognitive-complexity refactor of the same functions. |

## Workflow

Pre-commit hook to keep new code clean while legacy churns down separately:

```sh
golangci-lint run --new-from-rev=origin/main --config=./.golangci.yaml
```

Add this to `lefthook.yml` / `.pre-commit-config.yaml` / Husky, whichever the
project uses.

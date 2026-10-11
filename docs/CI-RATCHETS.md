# CI reliability gates

- Unit tests cover `pkg/` and `cmd/` with race detection and shuffle. The mock CSI
  sanity runner rejects failed specs and nonzero test exits regardless of the pass
  count; its current floor is 81. This is not real-storage integration coverage.
- `govulncheck` v1.9.0 scans reachable vulnerabilities on Go 1.27.2, matching the
  container builder. The vulnerability database changes over time; a future gate
  failure needs investigation, not suppression of the scan.
- `go mod tidy -diff` checks module tidiness without silently rewriting it.
- `nolint-budget.json` counts individual named linter suppressions across tracked
  Go files, including tests. Its total must match the source and cannot increase
  against `main`; lower it when removing suppressions. Blanket suppressions are
  forbidden. Per-linter reallocations remain visible in review.
- Statement coverage is compared per package against a fresh `main` run using
  the same OS, Go version and short-test scope. More than a 0.5 percentage-point
  decline fails. Empty profiles or missing baseline packages fail closed. This
  does not impose invented absolute floors: dashboard and CLI coverage currently
  start at zero and need meaningful tests rather than a false coverage claim.

Coverage baseline builds resolve the SDK revision recorded in `main`'s go.mod,
not a moving SDK checkout. Builds and publishing remain separate: images are
published only on explicit workflow dispatch.

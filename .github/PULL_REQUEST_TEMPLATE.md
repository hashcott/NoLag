## What and why

<!-- What does this change, and what problem does it solve? Link the issue: Fixes #123 -->

## How it was tested

- [ ] `go test ./...`
- [ ] `GOOS=windows go vet ./...`
- [ ] `./deploy/verify-firewall-in-docker.sh` (if `internal/agent` or `internal/ipsetsync` changed)
- [ ] Manual Windows checklist from `docs/windows-client-runbook.md` (if Windows client code changed)

## Checklist

- [ ] Tests cover the new or changed behaviour; a bug fix has a test that failed before it
- [ ] Docs updated in `docs/en/`, and in `docs/vi/` and `docs/zh-CN/` or noted below as not yet translated
- [ ] No boundary in CONTRIBUTING.md "Boundaries not to loosen" is changed, or a maintainer agreed to it in the issue

## Notes for the reviewer

<!-- Anything untranslated, anything untested, trade-offs you made. -->

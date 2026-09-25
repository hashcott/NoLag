# Contributing to GameNoLag

Thanks for helping. This page covers how to set up, what a change needs before
it is merged, and the conventions the codebase follows.

By taking part you agree to the [Code of Conduct](CODE_OF_CONDUCT.md). Report
security issues privately as described in [SECURITY.md](SECURITY.md), never in a
public issue.

## Setting up

You need Go 1.25 or newer. You also need Docker if you touch the relay
firewall.

```bash
git clone https://github.com/hashcott/NoLag.git && cd NoLag
go test ./...
GOOS=windows go vet ./...
./deploy/verify-firewall-in-docker.sh      # only for internal/agent, internal/ipsetsync
```

Everything builds and tests on macOS, Linux and Windows:

- Windows-only code sits behind `//go:build windows`.
- Tests that need root on Linux sit behind `//go:build linuxroot`.

So `go test ./...` never needs privilege and never touches the firewall.

## What a change needs

1. **An issue first for anything non-trivial**, so the approach is agreed
   before you spend time on it.
2. **Tests.** Logic that decides something has a test. A bug fix comes with a
   test that failed before the fix. Keep code that only calls the operating
   system thin, and put the decisions it needs in platform-free files that are
   tested everywhere. `cmd/gnl-ui/status.go`, `view.go` and `history.go` show
   the pattern.
3. **A green CI run:** `gofmt`, `go vet` (including the Windows build), and
   `go test` on Linux and Windows, plus the firewall tests against a real
   kernel.
4. **Docs updated with the behaviour.** English under `docs/en/` is the source
   of truth. If you cannot also update `docs/vi/` and `docs/zh-CN/`, say so in
   the pull request. A maintainer will mark those pages as out of date until
   they are translated.
5. **No new dependency** unless the pull request says why it is needed.

CI can check Windows-only code only with `go vet`, a build and unit tests. If
you change `cmd/gnl-ui/*_windows.go` or `cmd/gnl-service`, run the manual
checklist in the [Windows client runbook](docs/windows-client-runbook.md#the-window)
on a real Windows machine, and say so in the pull request.

## Boundaries not to loosen

A change to any of these needs a maintainer's explicit agreement in the issue
first:

- The pipe protocol: four verbs, no parameters (`internal/client/ipc`).
- Access control on the pipe, the install directory and `ProgramData`.
- The relay firewall: FORWARD policy DROP, the egress allowlist, the rate cap.
- The profile promotion rule: independent reporters and a published-range
  match.
- Anything that would make the client open, read or hook a game process.

## Style

- Run `gofmt`. Prefer the standard library; the dependency list is short on
  purpose.
- Comments explain **why**: the constraint, or the failure a line prevents. Not
  what the next line does.
- Errors carry context, as in `fmt.Errorf("control: record status: %w", err)`.
  Text a person will see tells them what to do next.

## Commits and pull requests

- Use [Conventional Commits](https://www.conventionalcommits.org/):
  `feat(client): …`, `fix(relay): …`, `docs: …`, `ci: …`,
  `test(control): …`.
- Keep the subject short and imperative. The body says why the change is
  needed and what it prevents.
- One logical change per pull request. Rebase on `main` rather than merging it
  in.
- Fill in the pull request template, including how you tested the change.

## License

By contributing you agree that your contribution is licensed under the
[Apache License 2.0](LICENSE), the license of this project.

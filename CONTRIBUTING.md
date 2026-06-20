# Contributing to ankerctl-ng

Thanks for your interest! `ankerctl-ng` is an experimental community fork of
[`Django1982/ankerctl_go_remake`](https://github.com/Django1982/ankerctl_go_remake),
itself a Go port of the Python [`Ankermgmt/ankerctl`](https://github.com/Ankermgmt/ankerctl).
Please read [NOTICE](NOTICE) for the attribution chain.

## Before you start

- This is a moving target aimed at people comfortable with experimental builds.
- For general (non-fork-specific) protocol questions, the upstream projects are
  often the better place to look first.
- By contributing you agree your changes are licensed under the project's
  [GNU GPLv3](LICENSE).

## Development setup

```sh
git clone https://github.com/jr551/ankerctl_go_remake.git
cd ankerctl_go_remake
sh scripts/install-hooks.sh        # installs a pre-commit hook (blocks commits to main)
bash scripts/prepare-web-vendor.sh # fetch vendored web assets
go build -o ankerctl-ng ./cmd/ankerctl/
```

## Working on changes

- **Never commit directly to `main`.** Create a feature branch:
  `git checkout -b my-change`.
- Keep commits atomic — one logical change each.
- Commit subjects: imperative, concise, <= 72 chars
  (e.g. `web: enforce API key on debug routes`).

## Code style

- Format all Go with `gofmt` before committing.
- Standard Go naming: exported `PascalCase`, unexported `camelCase`, short
  lowercase package names.
- Wrap errors explicitly (`fmt.Errorf("...: %w", err)`); avoid panics in
  production paths.
- Preserve protocol constants and crypto behavior exactly where documented —
  the project relies on bit-exact parity with the upstream implementations.

## Testing (required)

```sh
go test ./...          # full suite
go vet ./...           # static checks
```

- Add table-driven tests for protocol/model/config changes.
- Co-locate tests with code as `*_test.go`.
- Cover positive, negative, and edge cases for new logic.

## Security

- **Never** log or commit secrets (`auth_token`, `mqtt_key`, `api_key`),
  `config.json`, `login.json`, OAuth callback captures, or firmware blobs.
  These are gitignored — keep them that way.
- Report vulnerabilities per [SECURITY.md](SECURITY.md); do not open public
  issues for security problems.

## Pull requests

- Open PRs against `main`.
- Make sure `go test ./...` and `go vet ./...` pass and CI is green.
- Describe what changed and why; link any related issue.

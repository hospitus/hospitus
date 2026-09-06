# Contributing

The organization's rules — commit format, sign-off, and what a pull request is
expected to carry — live in
[hospitus/.github](https://github.com/hospitus/.github/blob/main/CONTRIBUTING.md)
and apply here.

Two files in this repository add to them:

- [`AGENTS.md`](AGENTS.md) is the authoritative source for code style, security
  invariants and test conventions. It applies to people and to coding agents
  alike. Read it before writing anything.
- [The developer guide](docs/book/src/developer-guide/contributing.md) covers
  setting up a FreeBSD host, the repository layout, how to add a provider, and
  how to run the tests that need real jails and VMs.

## The short version

```sh
make build          # hospitusd and hospitus
make test           # go test -short ./...
make lint           # golangci-lint, which must report nothing
go test -race ./...
```

`make test` passes `-short`. Anything needing a real FreeBSD host either gates
on `testing.Short()` or lives in `test/integration/`, which runs as root
against a real ZFS pool:

```sh
doas make test-integration
```

Jails and bhyve are FreeBSD-only, so most of the system cannot be exercised on
macOS or Linux; command-level behavior is covered there through `execx.Fake`
instead. Platform-divergent code uses build tags, and a change to one variant
usually needs the same change in its siblings.

## Reporting a bug

Open an issue using the bug report form. Security problems do not go in issues
— see [SECURITY.md](SECURITY.md).

## License

Contributions are licensed under the Apache License 2.0, the same terms as the
rest of the repository.

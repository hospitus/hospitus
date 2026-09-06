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

`make test` passes `-short`. Anything needing a real FreeBSD host gates on
`testing.Short()`, so the default run stays green on any platform.

Jails and bhyve are FreeBSD-only; QEMU and Podman also run on macOS and Linux.
What a provider does to the host therefore cannot be exercised everywhere, so
the command lines it builds are covered through `execx.Fake` instead.
Platform-divergent code uses build tags, and a change to one variant usually
needs the same change in its siblings, or the other builds break.

## Reporting a bug

Open an issue using the bug report form. Security problems do not go in issues
— see [SECURITY.md](SECURITY.md).

## License

Contributions are licensed under the Apache License 2.0, the same terms as the
rest of the repository.

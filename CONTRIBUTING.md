# Contributing to CredOrbit

Thanks for helping keep everyone's cloud access in orbit! 🛰️

## Development

```bash
git clone https://github.com/PiniPP/credorbit && cd credorbit
make build          # ./credorbit
make test race lint # what CI runs
go run ./cmd/credorbit ui --demo   # the TUI with a fake, offline service
```

- Go version: see `go.mod`.
- Never run tests against your real `~/.aws`; the suite uses temp dirs and fake SSO servers.
- Use only synthetic data (`acme-*`, `111111111111`, `https://d-1234567890.awsapps.com/start`).

## Pull requests

- `main` is protected: changes land through pull requests with passing CI
  (lint, tests on Linux/macOS/Windows, govulncheck).
- Use [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`).
- Keep the CLI and TUI thin: business logic belongs in `internal/engine` and the packages beneath it
  (see [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)).

## Licensing of contributions

CredOrbit is licensed under the [PolyForm Noncommercial License 1.0.0](LICENSE), with paid
[commercial licenses](COMMERCIAL.md) for companies. By submitting a contribution you agree that it
is licensed under the same terms and that the maintainer may include it in commercially licensed
distributions of CredOrbit.

## Security

Report vulnerabilities privately — see [.github/SECURITY.md](.github/SECURITY.md).

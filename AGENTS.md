# Repository Guidelines

## Project Structure & Module Organization

PeerGuard is a Go 1.24 peer-to-peer networking project. Executables live in `cmd/pgcli` (client, VPN, sharing, and administration) and `cmd/pgmap` (coordination server). Root packages are organized by responsibility: `disco/` handles discovery, `p2p/` and `rdt/` provide transport, `vpn/` implements virtual networking, and `secure/` contains cryptographic helpers. The Vue 3 UI lives in `peermap/ui`; assets are under `src/` and `public/`. Keep tests beside their package as `*_test.go`.

## Build, Test, and Development Commands

- `go test ./...` runs all Go package tests.
- `go build ./cmd/pgcli ./cmd/pgmap` verifies both command-line applications compile.
- `go run ./cmd/pgmap [flags]` runs the coordination server locally; inspect options with `-h`.
- `make ui` installs UI dependencies, formats its sources, and creates the production bundle.
- `cd peermap/ui && npm ci && npm run dev` starts the Vite development server using the locked dependency set.
- `make linuxamd64 version=dev` builds Linux AMD64 release binaries; see `Makefile` for other targets.

Run `go mod tidy` when imports change; commit `go.mod` and `go.sum` updates together.

## Coding Style & Naming Conventions

Format Go code with `gofmt` before committing. Follow standard Go conventions: tabs for indentation, short lowercase package names, exported identifiers in `PascalCase`, and internal identifiers in `camelCase`. Keep platform-specific implementations in suffix files such as `_linux.go`, `_windows.go`, and `_darwin.go`. For UI code, run `npm run format`; Prettier is the configured formatter. Match existing Vue component naming (`Signin.vue`) and use lowercase JavaScript module names.

## Testing Guidelines

Use Go's standard `testing` package and name tests `TestBehavior` in `*_test.go` files. Add focused regression tests near changed networking or concurrency code. There is no enforced coverage threshold; prioritize meaningful success, failure, timeout, and cleanup paths. Run `go test ./...` before every pull request, plus relevant platform builds for OS-specific changes.

## Commit & Pull Request Guidelines

Use a concise, imperative commit subject with an affected scope, for example `connmux: prevent accept-loop panic` or `peermap/ui: refine sign-in copy`. Keep commits focused. Pull requests should explain the problem and solution, list verification commands, and link related issues. Include screenshots for UI changes and call out platform, protocol, configuration, or compatibility impacts.

## Security & Configuration

Never commit private keys, generated secret files, tokens, or local peer configuration. Use environment variables such as `PG_SECRET_KEY` and `PG_SERVER` for local testing, and redact addresses or credentials from logs and screenshots.

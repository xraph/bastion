# AGENTS.md

Bastion is an API gateway that runs as a Forge extension. It proxies HTTP, WebSocket, SSE and gRPC, builds routes from FARP service discovery or from config, and adds load balancing, circuit breakers, health checks, rate limiting, retries, traffic splitting, auth, caching and an admin API on top.

The module is `github.com/xraph/bastion`, one `go.mod` at the root. The release workflow tags that root module and nothing else.

Bastion keeps its own version line. Its tags start at `v0.1.0` and the newest sit in the `v1.2.x` series, well below the `v1.x` line vault, warden and nexus are on, so you take bastion's next version from its own tags (`git tag --sort=-v:refname | head`). Don't bump it to match a sibling repo.

## Commands

Run these from the repo root. There's no `go.work`, and CI builds without one, so use `GOWORK=off` if a workspace above this checkout could leak in.

| What | Command |
|---|---|
| Build | `GOWORK=off go build ./...` |
| Tests | `make test` (`go test -v ./...`) |
| Race tests | `make test-race` |
| Coverage | `make coverage`, `make coverage-html` |
| Vet | `make vet` |
| Lint | `make lint`, or `make lint-fix` |
| gosec and govulncheck | `make sec` (installs either tool with `go install ...@latest` if it's missing) |
| Tidy | `make tidy` (`go mod tidy`, `go mod verify`) |
| Docs site | `make docs`, `make docs-build` |

Format with goimports directly:

```bash
gofmt -s -w . && goimports -w -local github.com/xraph/bastion .
```

Don't trust `make fmt` or `make check` for imports. The Makefile's `fmt` target passes `-local github.com/xraph/herald`, so it groups bastion's own imports with third-party ones, and CI's Format job (which uses `-local github.com/xraph/bastion`) then fails. `make build`, `make run`, `make install` and `make dev` point at `./cmd/bastion`, which doesn't exist. `make deps` also installs templ, which nothing in the repo uses.

This repo has no `.golangci.yml`, and no directory above it has one either. Both `make lint` and CI's Lint job run golangci-lint v2 with its default linter set (errcheck, govet, ineffassign, staticcheck, unused) and default settings. Import grouping is enforced only by the Format job.

CI is `.github/workflows/ci.yml`. The `ci` job calls the shared `xraph/workflows/.github/workflows/go-ci.yml@v1` on Go 1.26, Ubuntu only: `make test`, a race-enabled coverage run, golangci-lint, a `gofmt -l` check, `make vet`, a `go mod tidy` diff check on `go.mod` and `go.sum`, gosec (which fails the job on any finding) and govulncheck. Two jobs live in the repo itself. `format` fails on anything `goimports -l -local github.com/xraph/bastion .` lists. `docs` type-checks, lints and builds the Fumadocs site with pnpm.

### What the tests need

Nothing beyond Go. No test reads an env var, uses a build tag or starts a container, so `go test ./...` runs the whole suite.

It also means the database backends go untested. Only `store/memory` has tests; `store/postgres`, `store/sqlite` and `store/mongo` have none, so nothing you run locally or in CI exercises a real database. If you change one of those backends, you have to check it by hand against a real server.

## Layout

| Path | What lives there |
|---|---|
| `extension.go` | The root `Gateway` (embeds `*forge.BaseExtension`), its lifecycle and setters |
| `config.go`, `config_validate.go`, `config_watcher.go` | `Config`, `DefaultConfig`, the `ConfigOption` helpers, validation and hot reload |
| `config_octopus.go` | `OctopusConfig` and `FromOctopusConfig`, for octopus gateway YAML |
| `types.go`, `interfaces.go`, `store_interfaces.go` | Route and target types, the subsystem interfaces, the store interfaces |
| `hooks.go`, `middleware.go` | `HookEngine` (`OnRequest`, `OnResponse`, `OnError`, `OnRouteChange` and the rest) and gateway middleware |
| `extension_filter.go` | `IsExtensionPathExcluded`, which keeps other Forge extensions' paths out of discovered routes |
| `aliases.go`, `*_shim.go`, `observability_shims.go`, `*_compat.go` | Type aliases re-exported from the subpackages for existing `bastion.*` callers |
| `extension/` | The Forge extension most apps mount: wires concrete subsystems into the `Gateway` and picks a store |
| `extension/contract/` | Dashboard contract contributor and its `manifest.yaml` |
| `admin/` | Operator writes and the read models the REST API and the dashboard share |
| `routing/` | Route manager, load balancers, traffic splitting, versioning |
| `proxy/` | Proxy engine for HTTP, WebSocket, SSE and gRPC, plus stats |
| `resilience/` | Circuit breakers, retry, bulkhead, request coalescing, drain |
| `health/` | Active probes, passive failure tracking, history |
| `discovery/` | FARP service discovery, OpenAPI and AsyncAPI aggregation |
| `security/` | API key, bearer token, forward and JWT auth, admin auth, TLS, WAF |
| `middleware/` | Response caching, compression, rate limiter |
| `observability/` | Access log, audit, metrics |
| `plugin/` | Plugin manager and the GraphQL plugin |
| `api/` | WebSocket hub for real-time dashboard updates |
| `store/` | Composite `store.Store` and the `memory`, `sqlite`, `postgres`, `mongo` backends |
| `docs/` | Fumadocs site |

`MIGRATION.md` records how the dashboard moved from Go-rendered pages to the React plugin in forge-dashboard. `internal/httputil/` exists on disk with no tracked files.

## Conventions

The root package holds types and interfaces, and the subpackages hold implementations. `Gateway` talks to `RouteRegistry`, `CircuitControl`, `RetryPolicy`, `TrafficRouter`, `StatsRecorder` and the rest through interfaces in `interfaces.go`, and `extension/` injects the concrete ones (`routing.NewManager()`, `resilience.NewCBManager(...)` and so on) with setters such as `SetRouteRegistry`. The import graph has two sides. The root imports `discovery`, `health`, `middleware`, `observability` and `security` (for the aliases and shims), so those five must never import the root. `admin`, `api`, `plugin`, `proxy`, `resilience`, `routing`, `store/...` and `extension/...` import the root for its types, so the root must never import them outside tests. Before you add an import, check which side the package is on. New code imports the subpackage directly; the aliases in `aliases.go` exist for older callers.

You configure the gateway with functional options, `type ConfigOption func(*Config)`, starting from `DefaultConfig()` (`WithRoutes`, `WithRetry`, `WithCircuitBreaker`, `WithDiscovery` and many more in `config.go`). Forge loads file config under the `bastion` key. The extension's own options (`WithStore`, `WithGroveDatabase`, `WithDisableMigrate`) are `func(*Extension)` and go through `Configure`:

```go
ext := extension.New(bastion.WithRoutes(routes)).Configure(extension.WithGroveDatabase("main"))
```

The gateway declares a dependency on Forge's `discovery` extension, so register that extension too.

The store is optional. `extension/` uses `WithStore` first, then the named `*grove.DB` from `WithGroveDatabase`, then any `*grove.DB` in the container, and picks the backend from the grove driver name (`pg`, `sqlite`, `mongo`). With none of those, the gateway runs in memory. Every backend implements `store.Store`: the route, circuit breaker, health, cache, rate-limit and audit interfaces plus `Migrate`, `Ping` and `Close`. SQL backends register their migrations in a `migrate.NewGroup("bastion")` group. Append new migrations with the next `Version` and leave shipped ones alone.

Route IDs are UUID strings from `github.com/google/uuid`; this repo has no TypeID package. Routes carry a source. Only manual routes can be written through `admin`, because FARP and discovery rebuild theirs on every update, and a write to one of those returns a `*admin.SourceError`. `admin` reports bad input as `*admin.ValidationError` (with the field name), a missing route as `admin.ErrNotFound`, and a clash as `*admin.ConflictError`. `SourceError` unwraps to `admin.ErrNotManual` and `ConflictError` to `admin.ErrConflict`, so you can match on either the type with `errors.As` or the sentinel with `errors.Is`.

You log through the extension's forge logger with structured `forge.F` fields, as in `e.Logger().Info("bastion gateway registered", forge.F(...))`, and keep values out of the message string.

`extension/contract/manifest.yaml` lists the dashboard intents, and `manifest_test.go` checks it. Change the manifest, its handler and that test together.

## Dependencies

Bastion depends on these xraph modules (versions are in `go.mod`): `grove` with `drivers/mongodriver`, `drivers/pgdriver` and `drivers/sqlitedriver`, `forge`, `forge/extensions/discovery`, `farp`, `farp/discovery`, `go-utils` and `vessel`. No forgery repo imports bastion.

Bump one xraph module at a time, then tidy:

```bash
GOWORK=off go get github.com/xraph/forge@vX.Y.Z
GOWORK=off go get github.com/xraph/forge/extensions/discovery@vX.Y.Z
GOWORK=off go mod tidy
```

Forge tags `extensions/discovery` at the same version as its root, and grove does the same with its drivers, so move each family together. `farp/discovery` has its own tags; check that it still matches `farp` when you bump either. CI fails if `go.mod` or `go.sum` isn't tidy. Never commit a `replace` that points at a sibling checkout such as `../../farp`.

## Releasing

You release by dispatching the release workflow. Never push a tag by hand. Wait for CI on `main` to pass and for any upstream xraph release you depend on (forge, grove, farp), then run:

```bash
gh workflow run release.yml --ref main -f tag=vX.Y.Z
```

The tag keeps its leading `v`. `release.yml` creates the tag on the root module and pushes it, runs `go build ./...` and `go test -race -count=1 ./...`, and then creates a GitHub release with notes from `git log` since the previous tag. Mind the order. The tag goes out before the build and the race tests, so a failure there leaves a pushed tag with no GitHub release, and the fix ships as the next patch version.

Nothing downstream needs re-pinning, since no forgery repo depends on bastion.

## Branches

`main` has no branch protection and no rulesets, so a push lands directly. CI runs on pushes to `main` and on pull requests against it.

# Tyger — Copilot Instructions

Tyger is a REST API and CLI for **remote signal processing**. It accepts streaming
input, runs containerized user code on Kubernetes (cloud) or Docker (local), and
exposes the results via WORM data streams ("buffers"). It is deployed an
ASP.NET Core control-plane service (and a data plane service in local mode) plus
a set of Go-based CLIs and sidecars.

Always read the surrounding code before changing it, and prefer minimal,
targeted edits that match the conventions described below.

## Repository layout

| Path | Purpose |
| --- | --- |
| [server/](../server/) | .NET 10 solution ([tyger.sln](../server/tyger.sln)) |
| [server/ControlPlane/](../server/ControlPlane/) | `tyger-server` — runs jobs, manages codespecs/runs/buffers |
| [server/DataPlane/](../server/DataPlane/) | `tyger-data-plane` — local buffer I/O endpoint (Docker mode) |
| [server/Common/](../server/Common/) | Shared library (configuration, middleware, versioning, UDS) |
| [server/ControlPlane.UnitTests/](../server/ControlPlane.UnitTests/) | xUnit tests for the control plane |
| [cli/](../cli/) | Go module `github.com/microsoft/tyger/cli` (Go 1.26+) |
| [cli/cmd/tyger/](../cli/cmd/tyger/) | The `tyger` CLI entry point (cobra) |
| [cli/cmd/tyger-proxy/](../cli/cmd/tyger-proxy/) | Local HTTP/Unix-socket proxy to a remote Tyger API |
| [cli/cmd/buffer-sidecar/](../cli/cmd/buffer-sidecar/) | Sidecar that bridges named pipes <-> buffer storage |
| [cli/cmd/buffer-copier/](../cli/cmd/buffer-copier/) | Server-side cross-region buffer replication |
| [cli/cmd/worker-waiter/](../cli/cmd/worker-waiter/) | Init container for distributed runs |
| [cli/cmd/loader/](../cli/cmd/loader/) | Load/perf testing utility |
| [cli/integrationtest/](../cli/integrationtest/) | End-to-end tests (build tag `integrationtest`) |
| [cli/internal/install/](../cli/internal/install/) | `cloudinstall` (AKS/Azure) and `dockerinstall` (local) installers |
| [scripts/](../scripts/) | Shell helpers used by the Makefiles |
| [deploy/config/microsoft/](../deploy/config/microsoft/) | Source-of-truth dev/cloud/docker config templates |
| [deploy/helm/tyger/](../deploy/helm/tyger/) | Helm chart published with releases |
| [.devcontainer/](../.devcontainer/) | Dev container (Ubuntu + .NET + Go + Azure CLI + kubectl + helm) |

## Toolchain

- **.NET SDK** pinned in [server/global.json](../server/global.json) (`net10.0`,
  Nullable on, `TreatWarningsAsErrors=true`, `AnalysisMode=Recommended`,
  `RestorePackagesWithLockFile=true`). When you add or upgrade NuGet packages,
  update the corresponding `packages.lock.json` (run `make restore`).
- **Go** version is in [cli/go.mod](../cli/go.mod). Modules use a lock file
  (`go.sum`); run `go mod tidy` after dependency changes.
- **System tools** assumed on `PATH`: `make`, `bash`, `jq`, `yq`, `docker`,
  `kubectl`, `helm`, `az`, `psql`. The devcontainer installs all of these.

## Build, test, and format

The top-level [Makefile](../Makefile) is the canonical entry point for almost
every task. It selects between [Makefile.cloud](../Makefile.cloud) (default) and
[Makefile.docker](../Makefile.docker) based on the `TYGER_ENVIRONMENT_TYPE`
environment variable (`cloud` or `docker`). Some targets only exist in one of
the two; check both files before assuming a target is missing.

Common targets:

| Target | What it does |
| --- | --- |
| `make build` | `dotnet build server/tyger.sln` + `go build ./...` |
| `make build-csharp` / `make build-go` | One side only |
| `make restore` | `dotnet restore` + `go mod download` |
| `make format` | `dotnet format` (do this before pushing C# changes) |
| `make verify-format` | What CI runs; also enforces analyzer warnings |
| `make unit-test` | Runs all `*.csproj` tests and `go test ./...` (excludes the `integrationtest` build tag) |
| `make integration-test` | Brings the environment `up` and runs `cli/integrationtest` with `-tags=integrationtest` |
| `make integration-test-no-up` | Skips `up`; assumes a running deployment |
| `make integration-test-fast-only` | Adds `-fast` (skips long-running scenarios) |
| `make up` / `make down` | Install/uninstall Tyger into the target environment |
| `make install-cli` | `go install` the `tyger`, `tyger-proxy`, `buffer-sidecar` binaries with version + container image metadata baked in via `-ldflags` |
| `make run` | Run the control plane locally (after `make set-localsettings`) |
| `make full` | `make test INSTALL_CLOUD=true` (full CI-style run) |
| `make pretty-print-config-templates` | Regenerate the canonical config YAML; CI fails if the result differs |

Run the VS Code task `build` for fast incremental C# builds with the
`$msCompile` problem matcher (see [.vscode/tasks.json](../.vscode/tasks.json)).

The Go integration tests **require the `integrationtest` build tag**
(`go test -tags=integrationtest ./...` from `cli/integrationtest`). The
devcontainer's `gopls` is already configured with this tag.

## Typical inner-loop workflows

- **Changing Go code (CLI, sidecars, installers).** Edit, then
  `make install-cli` to rebuild and reinstall the `tyger`, `tyger-proxy`, and
  `buffer-sidecar` binaries on `$PATH` (with the right `-ldflags` baked in),
  then exercise the change by running `tyger ...` (or `tyger-proxy ...` /
  `buffer-sidecar ...`) directly. No `make up` is needed unless the change
  also requires a redeployed server.
- **Changing server code (C#).** The normal loop is `make up`, which builds
  and pushes the server image and (re)deploys it via Helm/Docker, then run
  `tyger ...` against it. For very fast inner iteration you can instead run
  the control plane locally with `make set-localsettings && make run` and
  point `tyger login` at `http://localhost:5000` (cloud) or the local Unix
  socket (docker). In cloud mode `make login-local` does this for you; in
  docker mode use `make login`.
- **Changing the API contract** (anything that touches DTOs, route handlers,
  or the OpenAPI surface): update both the C# model in
  `server/ControlPlane/Model/` and the Go model in
  [cli/internal/controlplane/model/](../cli/internal/controlplane/model/),
  bump the API version if it's a breaking change, then run
  `make integration-test-no-up-fast-only` so the OpenAPI snapshot diff runs.

## Coding conventions

### General

- Every source file has the standard MIT copyright header. If you create a new
  `.cs`, `.go`, `.sh`, `.ps1`/`.psm1`, or `Makefile`, add the matching header —
  [scripts/add-copyright-headers.sh](../scripts/add-copyright-headers.sh) can
  add missing headers automatically.
- `.editorconfig` is authoritative: 4-space indent for C#, tabs preserved in
  Makefiles, trim trailing whitespace, final newline required.
- Don't introduce new top-level dependencies casually — both projects use
  lockfiles and the dependency graph is reviewed.

### C# (server)

- Targets `net10.0`, nullable reference types enabled. Treat any new warning as
  a build break locally (`make verify-format` uses `-p:EnforceCodeStyleInBuild=true`).
- Naming (enforced by `.editorconfig`):
  - Public/protected/internal members: `PascalCase`.
  - Instance fields: `_camelCase` (leading underscore).
  - Static fields: `s_camelCase`.
  - Constants & non-private readonly fields: `PascalCase`.
  - Locals and parameters: `camelCase`.
- Use file-scoped namespaces, simple `using` statements, pattern matching,
  collection/object initializers, and predefined type keywords (`int`, not
  `Int32`). The analyzers will flag the alternatives.
- Wire new services through the existing `builder.AddXxx()` / `app.UseXxx()` /
  `app.MapXxx()` extension pattern visible in
  [server/ControlPlane/Program.cs](../server/ControlPlane/Program.cs). Each
  feature area (`AccessControl`, `Buffers`, `Codespecs`, `Compute`, `Database`,
  `Runs`, ...) lives in its own folder under `ControlPlane/`.
- API surface is versioned via `Tyger.ControlPlane.Versioning`; new endpoints
  should be registered inside `app.ConfigureVersionedRouteGroup(...)` and
  documented in OpenAPI (`server/ControlPlane/OpenApi/`). Integration tests
  diff the generated spec against
  [cli/integrationtest/expected_openapi_spec.yaml](../cli/integrationtest/expected_openapi_spec.yaml);
  regenerate it when changing the public API.
- Logging uses source-generated `LoggerExtensions` partial classes (see
  `Buffers/LoggerExtensions.cs` etc.) — add new log entries there rather than
  calling `_logger.LogInformation("literal")` ad hoc.

#### Data plane

- [server/DataPlane/](../server/DataPlane/) is its own minimal ASP.NET Core
  service (`tyger-data-plane`) that handles raw buffer I/O in docker mode. It
  shares `Tyger.Common` with the control plane and follows the same
  `builder.AddXxx()` / `app.UseXxx()` wiring (see
  [server/DataPlane/Program.cs](../server/DataPlane/Program.cs)), including its
  own `ConfigureVersionedRouteGroup` and `Tyger.DataPlane.Versioning`. It binds
  to a Unix domain socket and is configured by `make set-data-plane-localsettings`
  / run with `make run-data-plane`. Keep its surface small — buffer storage
  logic lives in `DataPlaneStorageHandler.cs`.

#### Database and migrations

- Database access lives in [server/ControlPlane/Database/](../server/ControlPlane/Database/)
  (`Repository.cs`, `Database.cs`). Schema changes are versioned, ordered
  migrations under
  [server/ControlPlane/Database/Migrations/](../server/ControlPlane/Database/Migrations/)
  (`1.cs`, `2.cs`, ...). To add one: create the next `MigratorN` class
  (subclass `Migrator`, implement `Apply`), then register it as a new entry in
  the `DatabaseVersion` enum in `Migrations/DatabaseVersions.cs` with
  `[Migrator(typeof(MigratorN))]` and a `[Description(...)]`.
- Migrations are designed to run **without downtime** (additive, idempotent
  SQL — e.g. `ADD COLUMN IF NOT EXISTS`); see
  [docs/reference/database-management.md](../docs/reference/database-management.md).
  They are applied via `tyger api migrations apply` (`make up` runs this in
  cloud mode) and listed with `tyger api migrations list`. The
  [cli/integrationtest/migrations_test.go](../cli/integrationtest/migrations_test.go)
  end-to-end test exercises this path, so run
  `make integration-test-no-up-fast-only` after touching migrations.

### Go (CLI)

- Use the existing logger: `github.com/rs/zerolog/log`. Don't add `fmt.Println`
  for diagnostics.
- Errors propagate via standard `error` returns; CLI top-level errors are
  printed by cobra. Use `log.Fatal().Err(err).Msg(...)` only at the program
  edges.
- New `tyger` subcommands plug into [cli/internal/cmd/rootcommand.go](../cli/internal/cmd/rootcommand.go).
  Follow the existing `NewXxxCommand() *cobra.Command` factory pattern, and use
  `cobra.Command.SilenceUsage = true` (inherited from the common root).
- Control-plane DTOs live in
  [cli/internal/controlplane/model/](../cli/internal/controlplane/model/) and
  are kept in lockstep with the server's `Tyger.ControlPlane.Model` types.
  Update both when changing the API.
- Integration test files **must** start with `//go:build integrationtest` and
  declare `package integrationtest`. Use the helpers in
  [cli/integrationtest/testutils.go](../cli/integrationtest/testutils.go)
  (`NewTygerCmdBuilder`, `runTyger`, `runCommandSucceeds`, etc.) instead of
  shelling out manually.

#### HTTP client transport (tread carefully)

The CLI talks to a Tyger endpoint over several connection types, all built in
[cli/internal/client/](../cli/internal/client/). This is one of the subtler
areas — understand the layering before changing it:

- `client.NewClient` (in [client.go](../cli/internal/client/client.go)) wraps a
  base `*http.Transport` in a stack of `http.RoundTripper`s (clock-skew check,
  optional debug logging) on top of a `retryablehttp.Client`. Layers are
  injected via `ClientOptions.CreateTransport` (`MakeRoundTripper`) and
  `CreateDialer` (`MakeDialer`) rather than by rewriting `NewClient`. Use
  `NewControlPlaneClient` / `NewDataPlaneClient` (the latter sets a longer
  timeout) instead of calling `NewClient` directly.
- The connection type is driven by the login URL scheme, surfaced by
  `TygerClient.ConnectionType()`: `http`/`https` (TCP), `http+unix` (Unix
  domain socket), `ssh`, and `docker`.
- **Unix sockets:** requests use the `http+unix` scheme with the socket path
  base32-encoded into the host (prefix `unix----`); `makeUnixDialer` /
  `unixAwareTransport` ([unixtransport.go](../cli/internal/client/unixtransport.go))
  decode it back to a `unix` dial. Don't put raw filesystem paths in the URL
  host.
- **SSH and Docker tunnels** are implemented as a `CommandTransport`
  ([commandtransport.go](../cli/internal/client/commandtransport.go)) that
  shells out to `ssh` / `docker`, writing the HTTP request to the process's
  stdin and reading the response from stdout. It only intercepts `http+unix`
  requests and otherwise delegates to `next`. URL parsing/formatting for these
  lives in [sshurl.go](../cli/internal/client/sshurl.go) and
  [dockerurl.go](../cli/internal/client/dockerurl.go); the transport is wired
  up in [cli/internal/controlplane/login.go](../cli/internal/controlplane/login.go).
- **Proxies:** `ParseProxy` accepts `none`, `auto`/`automatic`/`""`, or an
  explicit URL, and `httpCheckProxyFunc` deliberately bypasses proxies for
  plaintext `http` (the `tyger` → `tyger-proxy` path) unless it's a Unix socket.
- **Redaction:** never log raw URLs or HTTP errors — use `RedactUrl` /
  `RedactHttpError`, which strip query-string values (preserving only
  non-sensitive keys like `api-version`).

### Shell scripts

- All scripts begin with `#!/usr/bin/env bash` and `set -euo pipefail` (or
  `-ecuo pipefail` for the Makefile recipes). Keep that contract.
- Prefer the helpers in [scripts/](../scripts/) over re-implementing config
  lookup. Use `make get-config` to render the cloud/docker/dev configuration;
  fall back to `scripts/get-config.sh` directly only when you need its special
  options.

## Deployment model — short version

- **Cloud mode** (`Makefile.cloud`): the `tyger` CLI installs cloud
  infrastructure into Azure (`tyger cloud install`) and then the API
  (`tyger api install`) into an AKS cluster via Helm. Container images are
  pushed to an ACR identified by `wipContainerRegistry` in the dev config.
  Authentication uses Microsoft Entra (cert, managed identity, or GitHub
  federated identity, controlled by `TYGER_AUTH_METHOD`).
- **Docker mode** (`Makefile.docker`): everything runs locally as Docker
  containers; auth is disabled; the data plane runs alongside the control plane
  and they talk over Unix domain sockets under [install/local/](../install/).
- The Go installers live in
  [cli/internal/install/cloudinstall/](../cli/internal/install/cloudinstall/) and
  [cli/internal/install/dockerinstall/](../cli/internal/install/dockerinstall/).
  Configuration templates they consume are in
  [cli/internal/install/cloudinstall/](../cli/internal/install/cloudinstall/)
  and the [deploy/helm/tyger](../deploy/helm/tyger/) chart.

### Switching between cloud and docker mode

The repo is intentionally checked out **once** but exposed under two paths so
that VS Code can host a cloud window and a docker window side by side against
the same source tree:

- The devcontainer sets up a symlink `/workspaces/tyger-docker → /workspaces/tyger`
  (see [.devcontainer/Dockerfile](../.devcontainer/Dockerfile)).
- `make open-cloud-window` opens `code /workspaces/tyger` (cloud is the
  default — `TYGER_ENVIRONMENT_TYPE` is unset, so the top-level Makefile
  includes `Makefile.cloud`).
- `make open-docker-window` opens `code /workspaces/tyger-docker`.
  [.devcontainer/devcontainer.bashrc](../.devcontainer/devcontainer.bashrc)
  detects that path and exports `TYGER_ENVIRONMENT_TYPE=docker` plus a
  dedicated `TYGER_CACHE_FILE=~/.cache/tyger/.tyger-docker`. That alone is what
  flips the Makefile to `Makefile.docker` and keeps the docker-mode `tyger
  login` session isolated from the cloud one.
- [.vscode/launch.json](../.vscode/launch.json) has a matching **"CLI Docker"**
  debug configuration that points at the same cache file and uses
  `substitutePath` to translate `/workspaces/tyger-docker` ↔
  `/workspaces/tyger` for the debugger; use it when stepping into the CLI from
  the docker window.

Practical rules:

- Always open the docker workspace via `make open-docker-window` (or
  `code /workspaces/tyger-docker`) rather than `cd`-ing into the symlinked
  path from a cloud terminal — otherwise `TYGER_ENVIRONMENT_TYPE` won't be
  set and `make` will silently run the cloud recipes.
- You can verify which mode a terminal is in with `echo $TYGER_ENVIRONMENT_TYPE`;
  it should print `docker` in a docker window and be empty in a cloud window.
- Don't try to "support both modes" with a manually exported env var inside a
  cloud window. Open the dedicated window so the cache, env vars, and
  Makefile selection all line up.

## Pitfalls and house rules

- **Don't break the lockfiles.** After bumping NuGet or Go modules, regenerate
  both the manifest and the lockfile and commit them together.
- **Don't bypass the Makefile.** CI invokes the same targets you do; reproducing
  a CI failure usually means running the exact `make ...` target locally with
  the same `TYGER_ENVIRONMENT_TYPE`.
- **Don't add API fields without versioning.** The server uses
  `Tyger.ControlPlane.Versioning` and the CLI keeps a default version constant
  (`controlplane.DefaultApiVersion`). Add new fields behind a new minor version
  and update the OpenAPI snapshot test.
- **Don't manually edit `server/ControlPlane/appsettings.local.json`.** It is
  regenerated by `make set-localsettings` (in cloud mode from live Helm values,
  in docker mode from the rendered config); persistent changes belong in
  `appsettings.json` or the chart.
- **Don't disable analyzers or warnings as a quick fix.** The build treats
  warnings as errors on purpose; fix the underlying issue or scope a targeted
  suppression with a justifying comment.
- **Don't generate or commit secrets.** Test client certs come from Key Vault
  via `make download-test-client-cert`; never hard-code credentials or PII.
- **Don't run destructive `make` targets without confirming**: `down`,
  `remove-environment`, `purge-runs`, and `publish-official-images` all touch
  shared infrastructure.

## Quick start checklist for an edit

1. Identify the area: server (C#) or CLI/installer (Go).
2. Read the existing file and its siblings in the same folder; mimic the
   patterns (extension methods, naming, logging).
3. Make the change with the minimal diff.
4. Run `make build` and the relevant test target (`make unit-test` is usually
   enough; run `make integration-test-no-up-fast-only` if you touched the API
   contract or CLI commands).
5. Run `make verify-format` before declaring done.

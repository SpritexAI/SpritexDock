# SpritexDock

<p align="center">
  <strong>Lightweight deployment infrastructure for developers who want control.</strong>
</p>

<p align="center">
  A small, self-hosted PaaS inspired by Coolify — built to deploy applications without carrying an unnecessarily heavy control plane.
</p>

<p align="center">
  <a href="https://github.com/SpritexAI/SpritexDock/actions">CI</a> ·
  <a href="https://github.com/SpritexAI/SpritexDock/issues">Issues</a> ·
  <a href="https://github.com/SpritexAI">SpritexAI</a>
</p>

> **Project status:** Early development — not production-ready yet.

---

## The idea

Modern self-hosted deployment platforms are powerful, but their control planes can be too expensive for a small VPS, personal server, or developer workstation.

SpritexDock takes a narrower approach:

- one lightweight Go control-plane binary
- embedded SQLite state
- ephemeral sandboxed builds
- Docker-based application execution
- automatic HTTPS through Caddy
- a server-rendered dashboard instead of a heavy frontend runtime

The goal is not to reproduce every platform feature on day one. The goal is to make the essential path — **Git repository → build → deploy → HTTPS URL** — fast, transparent, and resource-conscious.

## What it will do

The MVP is being built around these workflows:

- Connect a Git repository
- Deploy manually or through a webhook
- Build in an ephemeral, resource-limited sandbox
- Stream build and deployment logs in real time
- Run and replace application containers safely
- Generate a public `sslip.io`-style URL automatically
- Attach a custom domain
- Obtain and renew HTTPS certificates through Caddy
- Manage environment variables without exposing secrets in logs

## Architecture

```text
                         Git provider webhook
                                  |
                                  v
┌───────────────────────────────────────────────────────────────┐
│                     SpritexDock control plane                 │
│                                                               │
│  Go API + dashboard · auth · SQLite · deployment orchestrator  │
│  log streaming · Docker client · Caddy route manager           │
└───────────────────────────────┬───────────────────────────────┘
                                │
                       Docker Engine API
                                │
              ┌─────────────────┴─────────────────┐
              │                                   │
              v                                   v
      Ephemeral build sandbox              Application container
       removed after build                  runs until replaced
                                │
                                v
                           Caddy proxy
                                │
                 generated URL / custom domain / HTTPS
```

### Design priorities

| Priority | Direction |
|---|---|
| **Low overhead** | Keep the control plane small and avoid mandatory Redis/Postgres/queue services. |
| **Safe execution** | Treat repositories and Dockerfiles as untrusted; isolate builds and apply limits. |
| **Fast feedback** | Stream logs and make deployment state visible while work is running. |
| **Simple operations** | Prefer one binary, one state file, and a small number of moving parts. |
| **Honest engineering** | Measure performance instead of promising benchmark numbers without evidence. |

## Technology direction

- **Backend:** Go
- **HTTP layer:** Fiber
- **State:** SQLite
- **Workload runtime:** Docker Engine
- **Proxy and TLS:** Caddy
- **Dashboard:** Go templates, htmx, Alpine.js, compiled Tailwind CSS
- **Verification:** GitHub Actions

The final choices for SQLite driver, build isolation backend, Caddy integration, secret protection, and supported host baseline are tracked as implementation decisions while the MVP is developed.

## Development status

SpritexDock is currently in the foundation phase. The approved product requirements define the MVP before implementation begins.

### Planned phases

- **Phase 1 — MVP:** single-server deployments, sandboxed builds, logs, generated URLs, custom domains, HTTPS, and webhooks
- **Phase 2 — Operational maturity:** private repositories, rollbacks, health checks, monitoring, backups, and API tokens
- **Phase 3 — Platform expansion:** multi-server agents, multi-service applications, database templates, teams, and alerting

## Repository layout

```text
cmd/server/          # application entry point
internal/            # API, auth, database, deployment, Docker, Git, logs, proxy
web/                 # server-rendered templates and static assets
migrations/          # database migrations
.github/workflows/   # CI, integration, release, and image workflows
test-app/            # separate local deployment-fixture repository (ignored)
```

## CI and branches

Development work is pushed to the `dev` branch. The `main` branch is reserved for manually approved merges.

GitHub Actions is the authoritative verification environment during the development phase. The workflow set is designed to validate Go, frontend, Docker-backed integration, release, and container-image changes without requiring a heavy local build environment.

## Try the deployment fixture

A small application will live in `test-app/` during development. It is intentionally a separate Git repository so SpritexDock can deploy a real external repository without adding the fixture to this project's history.

## Contributing

SpritexDock is being developed in the open by SpritexAI. Before contributing:

1. Read the approved product requirements.
2. Open an issue for a significant behavior or architecture change.
3. Keep security boundaries and failure handling explicit.
4. Add the smallest relevant automated test.
5. Push development work to `dev` and let GitHub Actions verify it.

The project is not accepting production deployment claims until the MVP acceptance criteria are complete.

## About SpritexAI

SpritexDock is an infrastructure project from **SpritexAI**, a cognitive AI infrastructure and deep research lab engineering orchestration layers, cognitive models, and practical developer systems for next-generation digital intelligence.

SpritexAI is also the home of the **RexiO** product ecosystem — including RexiO, a multilingual AI assistant designed to understand Bangla, Banglish, and English.

- [SpritexAI](https://spritexai.pro.bd)
- [SpritexAI on GitHub](https://github.com/SpritexAI)
- [RexiO](https://rexio.pro)
- [Mohammad Sijan — Founder and Lead Architect](https://sijan.pro.bd)

## License

License terms will be added before the first public release.

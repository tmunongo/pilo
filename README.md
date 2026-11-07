# 🛰️ Pilo

<p align="center">
  <strong>A lightweight, self-hosted PaaS for deploying static web applications straight from GitHub.</strong>
</p>

<p align="center">
  <a href="https://github.com/tmunongo/pilo/actions/workflows/ci.yml">
    <img src="https://github.com/tmunongo/pilo/actions/workflows/ci.yml/badge.svg" alt="CI Status" />
  </a>
  <a href="https://github.com/tmunongo/pilo/actions/workflows/release.yml">
    <img src="https://github.com/tmunongo/pilo/actions/workflows/release.yml/badge.svg" alt="Release Status" />
  </a>
  <a href="https://github.com/tmunongo/pilo/releases">
    <img src="https://img.shields.io/github/v/release/tmunongo/pilo?color=blue&logo=github" alt="GitHub Release" />
  </a>
  <a href="https://ghcr.io/tmunongo/pilo">
    <img src="https://img.shields.io/badge/docker-ghcr.io%2Ftmunongo%2Fpilo-blue?logo=docker" alt="Docker Container Image" />
  </a>
  <a href="https://golang.org">
    <img src="https://img.shields.io/github/go-mod/go-version/tmunongo/pilo" alt="Go Version" />
  </a>
</p>

---

Pilo turns any machine (VPS, home server, or Raspberry Pi) into a micro-PaaS. It listens for incoming HTTP requests, dynamic routes requests to cloned GitHub repositories on disk, and supports automated deployment via GitHub webhooks.

```
                  ┌────────────────────────────────────────┐
                  │              GitHub Repo               │
                  └──────────────────┬─────────────────────┘
                                     │
                             (Push / Webhook)
                                     │
                                     ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                              Pilo Server                                │
├─────────────────────────┬───────────────────────┬───────────────────────┤
│    Admin UI (/)         │   /app-one/*          │   /app-two/*          │
├─────────────────────────┼───────────────────────┼───────────────────────┤
│  Manage Apps & Webhooks │  /data/repos/app-one  │  /data/repos/app-two  │
└─────────────────────────┴───────────────────────┴───────────────────────┘
```

---

## ✨ Features

- ⚡ **Zero-Dependencies Runtime**: Single Go binary powered by embedded SQLite storage (`modernc.org/sqlite`).
- 👤 **Multi-User Authentication**: Secure sign up, sign in, session management, and password resets via bcrypt.
- 🛡️ **Account-Scoped Isolation**: Projects and GitHub credentials are fully isolated per user account.
- 🔄 **Dynamic Routing**: Instant route registration without restarting the server.
- 🔐 **GitHub Webhook Sync**: Verified HMAC `X-Hub-Signature-256` payload processing for auto-pulling main branches.
- 📦 **Multi-Arch Docker Images**: Native support for `amd64` and `arm64` (Raspberry Pi).
- 🔒 **Production Ready**: Zero-downtime deployments via Kamal with automated Let's Encrypt SSL, or behind any reverse proxy.

---

## 🚀 Quick Start (Local Development)

### Using Makefile

```bash
# Clone the repository
git clone https://github.com/tmunongo/pilo.git
cd pilo

# Run locally (starts on http://localhost:8080)
make run
```

### Using Go CLI

```bash
# Fetch dependencies
go mod tidy

# Run locally
PILO_PORT=8080 go run main.go
```

Open `http://localhost:8080` in your browser and configure your GitHub Personal Access Token (PAT) under Settings.

---

## 🏷️ Versioning & Automated Releases

Pilo uses **Semantic Versioning (SemVer)** automated via Makefile recipes and GitHub Actions.

### Inspect Current & Next Versions

```bash
make version
```

### Trigger a Release

Bump the version and automatically push a Git tag (`vX.Y.Z`). This triggers the GitHub Actions release workflow to build multi-platform Docker images (`linux/amd64`, `linux/arm64`) and publish them to **GitHub Container Registry (`ghcr.io/tmunongo/pilo`)**.

```bash
# Create and push a patch release (v0.0.1 -> v0.0.2)
make patch

# Create and push a minor release (v0.0.1 -> v0.1.0)
make minor

# Create and push a major release (v0.0.1 -> v1.0.0)
make major
```

---

## 🐳 Deployment Options

### Option 1: Standalone Docker Run

Run Pilo as a background Docker container with persistent volume storage for database and cloned repositories:

```bash
docker run -d \
  --name pilo \
  --restart unless-stopped \
  -p 8080:8080 \
  -v pilo-data:/data \
  -e PILO_PORT=8080 \
  -e PILO_DATA_DIR=/data \
  -e PILO_BASE_URL=http://localhost:8080 \
  ghcr.io/tmunongo/pilo:latest
```

---

### Option 2: Docker Compose

Create a [`docker-compose.yml`](file:///Users/tawanda/Projects/pilo/docker-compose.yml) file:

```yaml
version: '3.8'

services:
  pilo:
    image: ghcr.io/tmunongo/pilo:latest
    container_name: pilo
    restart: unless-stopped
    ports:
      - "8080:8080"
    environment:
      - PILO_PORT=8080
      - PILO_DATA_DIR=/data
      - PILO_BASE_URL=http://localhost:8080
    volumes:
      - pilo-data:/data

volumes:
  pilo-data:
    driver: local
```

Start the container:

```bash
docker compose up -d
```

---

### Option 3: Production Deployment with Kamal (Automated Zero-Downtime & SSL)

Pilo is configured for zero-downtime production deployments with [Kamal](https://kamal-deploy.org/) using `kamal-proxy` for SSL termination and traffic routing.

#### Step 1: Configure Credentials

Ensure `.env.production` contains your GitHub Container Registry token:

```bash
KAMAL_REGISTRY_PASSWORD=ghp_your_personal_access_token
```

#### Step 2: Deploy

Deploy to your production server:

```bash
./bin/kamal deploy
```

`kamal-proxy` automatically provisions Let's Encrypt certificates, manages zero-downtime rolling deploys, and routes requests to Pilo.

---

## ⚙️ Environment Variables

| Variable | Default | Description |
| :--- | :--- | :--- |
| `PILO_DATA_DIR` | `./data` | File path where SQLite database (`paas.db`) and cloned Git repositories are stored |
| `PILO_PORT` | `8080` | Internal HTTP listening port for the Go server |
| `PILO_BASE_URL` | `http://localhost:<PORT>` | Public base URL used for rendering GitHub webhook endpoints & reset links |
| `SMTP_HOST` | _(empty)_ | Optional SMTP host for sending password reset emails (logs link to stdout when empty) |
| `SMTP_PORT` | `587` | SMTP port |
| `SMTP_USER` | _(empty)_ | SMTP authentication username |
| `SMTP_PASS` | _(empty)_ | SMTP authentication password |
| `SMTP_FROM` | `noreply@pilo.local` | From address on outbound reset emails |

---

## 🛠️ GitHub Webhooks Integration

1. In Pilo's settings page (`/`), configure a **Webhook Secret**.
2. In your target GitHub repository, go to **Settings > Webhooks > Add webhook**.
3. Set **Payload URL** to `https://pilo.yourdomain.com/webhooks/github` (or `http://your-server-ip:8080/webhooks/github`).
4. Set **Content type** to `application/json`.
5. Enter the same **Secret** you defined in Pilo.
6. On every `push` event, Pilo verifies the `X-Hub-Signature-256` signature and automatically runs `git pull` on the targeted project branch.

---

## 📂 Data Layout

```
/data/
├── paas.db          ← SQLite database (Projects, Settings)
└── repos/           ← Managed GitHub repositories
    ├── my-app/      ← git clone of owner/repo@branch
    └── docs-site/   ← git clone of owner/repo@branch
```

---

## 🛠️ Developer Commands

| Command | Description |
| :--- | :--- |
| `make help` | Show all available Makefile commands |
| `make sqlc` | Generate type-safe Go database code from SQL schema/queries using `sqlc` |
| `make test` | Run Go unit test suite (`go test -v ./...`) |
| `make build` | Build standalone executable (`pilo`) |
| `make docker-build` | Build local Docker container |
| `make patch` | Tag and publish next patch release (`vX.Y.Z+1`) |
| `make minor` | Tag and publish next minor release (`vX.Y+1.0`) |
| `make major` | Tag and publish next major release (`vX+1.0.0`) |

# 🛰️ Pilo

A tiny, self-hosted PaaS for deploying static (HTML/CSS/JS) tools straight from GitHub onto a local URL path. One Go binary, SQLite storage, no framework needed.

```
GitHub repo → cloned onto disk → served at a URL path
```

## Quick start

```bash
# 1. Fetch the SQLite driver (requires network)
go mod tidy

# 2. Run locally
PILO_PORT=8080 go run main.go

# 3. Open http://localhost:8080 and add your GitHub PAT in Settings
```

## Cross-compile for Raspberry Pi (ARM64)

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o pilo-arm64 main.go
scp pilo-arm64 pi@pilo.local:/home/pi/pilo
```

## Docker

```bash
docker build -t pilo .
docker run -d -p 8080:8080 -v pilo-data:/data --name pilo pilo
```

## How it works

| URL path         | Served from                        |
|------------------|------------------------------------|
| `/my-app/*`      | `/data/repos/my-app/` (or subdir) |
| `/another-tool/*`| `/data/repos/another-tool/`       |
| `/`              | Admin UI                           |

Routes are registered dynamically as you add projects — no restart needed.

## Data layout

```
/data/
  paas.db          ← SQLite (projects + settings)
  repos/
    my-app/        ← git clone of owner/repo@branch
    another-tool/
```

## Environment variables

| Variable         | Default                    | Description                     |
|------------------|----------------------------|---------------------------------|
| `PILO_DATA_DIR`  | `./data`                   | Where to store the DB and repos |
| `PILO_PORT`      | `8080`                     | HTTP listen port                |
| `PILO_BASE_URL`  | `http://localhost:<PORT>`  | Used to render the webhook URL  |

## GitHub webhooks (VPS mode)

Point a repo webhook at `http://your-server:8080/webhooks/github` with content type `application/json`. Set a **Webhook Secret** in Pilo's Settings page and paste the same value into GitHub's webhook config.

On push to a tracked branch, Pilo verifies the `X-Hub-Signature-256` HMAC and runs `git pull` automatically.

On the Pi (no public IP) you skip webhooks and use the manual **Redeploy** button instead.

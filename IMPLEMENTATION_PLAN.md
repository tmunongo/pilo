# User-scoped projects

Plan: use `/projects/<namespace>/<slug>/` for public sites. The namespace is an optional, normalized, unique username, falling back to the account's random UUID. Keep authenticated management URLs at `/projects/<slug>` and checkout directories at `repos/<user UUID>/<slug>` so username changes do not move files. Existing public URLs change; update bookmarks and any site base paths.

To do:
- [x] Migrate the database to unique usernames and per-user project slugs.
- [x] Add optional usernames at signup and editable usernames in settings.
- [x] Scope public routing, checkouts, deploys, deletion, and site links by owner.
- [x] Provide a resumable checkout migration command/script and deployment instructions.
- [x] Test same-name projects, username conflicts/changes, and legacy migration; run Go checks.

Validation: `go test -race ./...`, `go vet ./...`, and `git diff --check` pass. Production rollout remains an operator step: stop the old service, back up the data volume, run the migration with the new binary, then start the new service. See scripts/migrate-projects.md for exact commands.

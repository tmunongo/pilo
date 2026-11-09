# One-time project namespace migration

For an existing deployment:

1. Stop the old Pilo process/container, including during a Kamal upgrade. This
   migration needs downtime: do not overlap old and new app versions.
2. Back up the entire persistent data directory/volume, including `paas.db` and
   `repos`. Keep that backup to roll back the database and files together.
3. Build the updated binary, then run the migration with the same data directory:

   ```sh
   go build -o /tmp/pilo-upgrade main.go
   PILO_BINARY=/tmp/pilo-upgrade PILO_DATA_DIR=/path/to/data ./scripts/migrate-projects.sh
   ```

   With Docker, after stopping the existing app, run the new image against the
   same volume (replace the image tag with your built/published version):

   ```sh
   docker run --rm -v pilo:/data ghcr.io/tmunongo/pilo:<new-tag> /app/pilo --migrate-projects
   ```

4. Start the updated app against that data directory. Verify the new site URLs in
   project details. Update bookmarks and any hardcoded asset/base paths in sites;
   old `/<slug>/` URLs are no longer served.

The migration command applies embedded database migration `004_project_namespaces`
and renames existing checkouts, preserving Git history and files. It is rerunnable:
completed moves are skipped, conflicting destinations are never overwritten, and a
`repos/.project-namespaces-v1` marker records completion. Startup also runs this
migration if the marker is absent. Missing checkouts are left for redeployment.
If it reports an unowned legacy project, provision the initial account using
`PILO_ADMIN_EMAIL` and `PILO_ADMIN_PASSWORD` on the migration command so the existing
first-account adoption runs, or explicitly assign the project to its intended user
before retrying. For rollback, stop the app and restore the entire backup before
starting the old binary.

#!/bin/sh
# Instructions: scripts/migrate-projects.md
# Stop Pilo and back up PILO_DATA_DIR before running against production data.
set -eu
exec "${PILO_BINARY:-./pilo}" --migrate-projects

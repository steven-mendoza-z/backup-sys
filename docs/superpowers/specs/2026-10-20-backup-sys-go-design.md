# Backup-sys Go Migration Design

## Goal

Replace the Python backup runner with Go while preserving the container interface and existing backup compatibility for consumers such as Agronet.

## Design

### CLI contract

- Keep the required subcommands `schedule`, `backup`, and `restore`; unknown/missing commands and invalid flags fail non-zero. Preserve standard help output and do not add a version command.
- `backup` and `schedule` accept optional `--frequency`; when omitted, use `BACKUP_FREQUENCY`, default `daily`.
- `restore` requires `--timestamp` in UTC `YYYYMMDDTHHMMSSZ` form, accepts optional `--frequency` with the same default, and repeatable `--database` flags. Database values must exactly match configured aliases; reject any unknown alias before downloading or modifying databases. No database flags means restore every configured database.
- One-shot success exits 0; config, validation, subprocess, or storage failures exit non-zero. Scheduled backup failures are logged and retried at the next interval.

### Configuration baseline

- `BACKUP_PROJECT`, falling back to `PROJECT_NAME`, then `project`; allow letters, numbers, dots, underscores, and hyphens except `.` and `..`.
- `BACKUP_EXECUTION_MODE=tcp` by default; `docker-compose` mode runs PostgreSQL client commands inside each configured Compose service from the VM binary. `BACKUP_COMPOSE_DIRECTORY` defaults to `.`, `BACKUP_COMPOSE_FILE` to `docker-compose.yml`, and optional `BACKUP_COMPOSE_ENV_FILE` overrides Compose's default `.env` lookup.
- `BACKUP_DIR=/backups`; `BACKUP_DATABASES` is required and contains unique aliases matching letters, numbers, `_`, or `-`. Alias environment prefixes uppercase aliases and replace `-` with `_`.
- Each `BACKUP_DB_<ALIAS>_NAME` and `_USER` is required. TCP mode also requires `_HOST` and `_PASSWORD`; `_PORT=5432`, `_SSLMODE`, and in Compose mode `_SERVICE` and optional `_PASSWORD_ENV=POSTGRES_PASSWORD` are supported. Port must be 1–65535. Compose mode runs on the VM and requires Docker Compose access; it does not require PostgreSQL client binaries installed on the VM.
- `BACKUP_LOCAL_RETENTION_COUNT=3` (minimum 1), `BACKUP_INTERVAL_SECONDS=86400` (minimum 60), and `BACKUP_RUN_ON_START=true`. Boolean parsing accepts true/false, 1/0, yes/no, case-insensitively.
- `BACKUP_STORAGE_PROVIDER` and `BACKUP_STORAGE_BUCKET` must both be set or both empty. Provider is empty, `s3`, `r2`, or `gcs`. Prefix is optional and trimmed of leading/trailing slashes. R2 requires `R2_ENDPOINT_URL` with trailing slashes removed.

### Backup and restore behavior

- Timestamp format is UTC `YYYYMMDDTHHMMSSZ`; files are `<BACKUP_PROJECT>-<alias>-<timestamp>.dump` under `<BACKUP_DIR>/<frequency>/`, with frequency restricted to letters, numbers, `_`, and `-`.
- TCP mode uses PostgreSQL custom-format dumps via `pg_dump --host H --port P --username U --format=custom --no-password --file FILE DB`, setting `PGPASSWORD` and optional `PGSSLMODE` in the child environment. Compose mode invokes the same PostgreSQL tools with `docker compose exec -T SERVICE sh -c ...`; dump output streams to a local file and restore input streams from a local file. A fixed shell wrapper reads the configured password environment variable from inside the database service and executes the tool with positional arguments, keeping credentials out of command-line arguments and the VM runner's environment.
- Write each dump to a `.dump.tmp` path and atomically rename on success. Upload all completed database dumps to `<prefix>/<project>/postgres/<frequency>/<filename>` when remote storage is configured. Prune local files by modification time after successful uploads, retaining the newest configured count per alias and frequency.
- Restore first resolves all requested files locally, downloading missing files from the configured remote provider; if no remote provider is configured or an object is missing, fail before dropping any database. Then for each selected database run `dropdb --host H --port P --username U --if-exists --force DB`, `createdb --host H --port P --username U --owner U DB`, then `pg_restore --host H --port P --username U --dbname DB --no-owner --no-acl --exit-on-error FILE`, using the same child environment. Compose mode runs those commands inside the configured service and streams the dump to `pg_restore` stdin. Restore is destructive and can partially complete if a later database fails.
- S3 and R2 use AWS credential-chain environment/shared configuration (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, optional session token and profile). Region precedence is `AWS_DEFAULT_REGION`, `AWS_REGION`, then `auto` for a custom endpoint or `us-east-1` otherwise. R2 uses the configured endpoint; ordinary S3 uses the default endpoint. GCS uses Google ADC, supporting `GOOGLE_APPLICATION_CREDENTIALS` or the runtime identity.
- Scheduled mode runs immediately unless `BACKUP_RUN_ON_START=false`, then sleeps `BACKUP_INTERVAL_SECONDS` between attempts. It logs a failed attempt and proceeds to the next scheduled attempt.

### Runtime and publishing

- Build static Linux amd64 and arm64 release binaries for VM/Compose mode, and keep the Docker image as an alternative for TCP mode. The image uses a multi-stage build and retains PostgreSQL client tools (`pg_dump`, `dropdb`, `createdb`, `pg_restore`), UID/GID 10001 non-root execution, `/backups` volume, and an entrypoint that accepts the CLI contract above with default command `schedule`.
- Compose mode requires the VM user running backup-sys to have access to Docker. Docker daemon access is equivalent to host administrator access; use a dedicated trusted deployment account and do not mount the Docker socket into the backup-sys image.
- Remove Python source and dependency manifests after the Go implementation is complete; update README and the GHCR workflow while preserving release tags `v*`, publishing the semver image tag without `v` plus `latest`, and attaching Linux binaries and checksums to the GitHub Release.

## Compatibility and Failure Behavior

Existing backup files remain readable because the dump format, names, timestamps, paths, and object keys do not change. The command and environment contracts remain compatible with the current Docker image consumers. Configuration errors and operational failures exit non-zero for one-shot commands. Scheduled backup failures are logged and retried on the next interval. Restore remains destructive and may partially complete if a later database restore fails.

## Validation

Compare the Go implementation against this Python behavior baseline for CLI/config parsing, aliases/defaults, naming and object-key generation, retention ordering, remote download fallback, subprocess argument/environment construction, and scheduler retries. Run Go formatting and static compilation checks where available. Do not require cloud credentials or perform real backup/restore operations as part of migration validation.

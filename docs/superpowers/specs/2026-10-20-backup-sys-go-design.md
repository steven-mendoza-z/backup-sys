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
- `BACKUP_DIR=/backups`; `BACKUP_DATABASES` is required and contains unique aliases matching letters, numbers, `_`, or `-`. Alias environment prefixes uppercase aliases and replace `-` with `_`.
- Each `BACKUP_DB_<ALIAS>_HOST`, `_NAME`, `_USER`, and `_PASSWORD` is required; `_PORT=5432` and `_SSLMODE` is optional. Port must be 1–65535.
- `BACKUP_LOCAL_RETENTION_COUNT=3` (minimum 1), `BACKUP_INTERVAL_SECONDS=86400` (minimum 60), and `BACKUP_RUN_ON_START=true`. Boolean parsing accepts true/false, 1/0, yes/no, case-insensitively.
- `BACKUP_STORAGE_PROVIDER` and `BACKUP_STORAGE_BUCKET` must both be set or both empty. Provider is empty, `s3`, `r2`, or `gcs`. Prefix is optional and trimmed of leading/trailing slashes. R2 requires `R2_ENDPOINT_URL` with trailing slashes removed.

### Backup and restore behavior

- Timestamp format is UTC `YYYYMMDDTHHMMSSZ`; files are `<BACKUP_PROJECT>-<alias>-<timestamp>.dump` under `<BACKUP_DIR>/<frequency>/`, with frequency restricted to letters, numbers, `_`, and `-`.
- Use PostgreSQL custom-format dumps via `pg_dump --host H --port P --username U --format=custom --no-password --file FILE DB`, setting `PGPASSWORD` and optional `PGSSLMODE` in the child environment.
- Write each dump to a `.dump.tmp` path and atomically rename on success. Upload all completed database dumps to `<prefix>/<project>/postgres/<frequency>/<filename>` when remote storage is configured. Prune local files by modification time after successful uploads, retaining the newest configured count per alias and frequency.
- Restore first resolves all requested files locally, downloading missing files from the configured remote provider; if no remote provider is configured or an object is missing, fail before dropping any database. Then for each selected database run `dropdb --host H --port P --username U --if-exists --force DB`, `createdb --host H --port P --username U --owner U DB`, then `pg_restore --host H --port P --username U --dbname DB --no-owner --no-acl --exit-on-error FILE`, using the same child environment. Restore is destructive and can partially complete if a later database fails.
- S3 and R2 use AWS credential-chain environment/shared configuration (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, optional session token and profile). Region precedence is `AWS_DEFAULT_REGION`, `AWS_REGION`, then `auto` for a custom endpoint or `us-east-1` otherwise. R2 uses the configured endpoint; ordinary S3 uses the default endpoint. GCS uses Google ADC, supporting `GOOGLE_APPLICATION_CREDENTIALS` or the runtime identity.
- Scheduled mode runs immediately unless `BACKUP_RUN_ON_START=false`, then sleeps `BACKUP_INTERVAL_SECONDS` between attempts. It logs a failed attempt and proceeds to the next scheduled attempt.

### Runtime and publishing

- Build a static Go executable in a Docker multi-stage build. The runtime image retains PostgreSQL client tools (`pg_dump`, `dropdb`, `createdb`, `pg_restore`), UID/GID 10001 non-root execution, `/backups` volume, and an entrypoint that accepts the CLI contract above with default command `schedule`.
- Remove Python source and dependency manifests after the Go implementation is complete; update README and the GHCR workflow while preserving release tags `v*`, publishing the semver tag without `v` plus `latest`, and building the repository root Dockerfile.

## Compatibility and Failure Behavior

Existing backup files remain readable because the dump format, names, timestamps, paths, and object keys do not change. The command and environment contracts remain compatible with the current Docker image consumers. Configuration errors and operational failures exit non-zero for one-shot commands. Scheduled backup failures are logged and retried on the next interval. Restore remains destructive and may partially complete if a later database restore fails.

## Validation

Compare the Go implementation against this Python behavior baseline for CLI/config parsing, aliases/defaults, naming and object-key generation, retention ordering, remote download fallback, subprocess argument/environment construction, and scheduler retries. Run Go formatting and static compilation checks where available. Do not require cloud credentials or perform real backup/restore operations as part of migration validation.

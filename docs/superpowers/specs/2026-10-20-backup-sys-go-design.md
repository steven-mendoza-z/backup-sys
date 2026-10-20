# Backup-sys Go Migration Design

## Goal

Replace the Python backup runner with Go while preserving the container interface and existing backup compatibility for consumers such as Agronet.

## Design

- Implement a Go CLI with `schedule`, `backup --frequency`, and `restore --timestamp --frequency [--database]` commands.
- Keep all current environment variable names and defaults, alias validation, UTC timestamps, backup filenames, frequency directories, and object key layout (`<prefix>/<project>/postgres/<frequency>/<filename>`).
- Continue using PostgreSQL custom-format dumps via `pg_dump`; restores use `dropdb`, `createdb`, and `pg_restore` with the current destructive replacement semantics.
- Use Go AWS SDK support for S3 and R2, including current AWS region/endpoint conventions, and Google Cloud Storage SDK with Application Default Credentials.
- Preserve local retention, remote upload/download fallback during restore, and scheduler retry behavior.
- Build a static Go executable in a Docker multi-stage build. The runtime image retains PostgreSQL client tools, a non-root user, `/backups`, and the existing entrypoint command shape.
- Remove Python source and dependency manifests after the Go implementation is complete; update README and GHCR publishing workflow for Go builds.

## Compatibility and Failure Behavior

Existing backup files remain readable because the dump format, names, timestamps, paths, and object keys do not change. Configuration errors and operational failures exit non-zero for one-shot commands. Scheduled backup failures are logged and retried on the next interval. Restore remains destructive and may partially complete if a later database restore fails.

## Validation

Review the migrated configuration, storage, subprocess invocation, scheduler, and CLI paths. Run formatting and static compilation checks where available. Do not require cloud credentials or perform real backup/restore operations as part of migration validation.

# backup-sys

Reusable PostgreSQL backup and restore runner distributed as Linux binaries and an optional Docker image. It supports direct TCP connections or execution inside a PostgreSQL Docker Compose service.

The runner is implemented in Go. The image uses a multi-stage build and includes only the compiled runner, PostgreSQL client tools, and CA certificates at runtime.

## Release

Push a tag such as `v1.0.0`. GitHub Actions publishes the optional Docker image `ghcr.io/steven-mendoza-z/backup-sys:1.0.0` plus `:latest`, and creates Linux amd64/arm64 binaries with checksums as workflow artifacts. Configure the GHCR package as public, or grant pull access to each consuming server. Workflow artifacts are temporary; download them from the tagged workflow run.

Build the image locally with `docker build -t backup-sys:local .`.

## Run on a VM with Compose PostgreSQL

For projects whose PostgreSQL server already runs in Compose, run the Linux binary on the VM with `BACKUP_EXECUTION_MODE=docker-compose`. The runner uses `docker compose exec` to execute `pg_dump`, `dropdb`, `createdb`, and `pg_restore` inside the configured database service. PostgreSQL client tools do not need to be installed on the VM, and no backup container or Docker socket mount is needed.

Example environment for one database:

```dotenv
BACKUP_EXECUTION_MODE=docker-compose
BACKUP_PROJECT=inventory
BACKUP_DATABASES=app
BACKUP_DB_APP_SERVICE=postgres
BACKUP_DB_APP_NAME=inventory
BACKUP_DB_APP_USER=inventory
BACKUP_DB_APP_PASSWORD_ENV=POSTGRES_PASSWORD
BACKUP_DIR=/var/backups/inventory
BACKUP_STORAGE_PROVIDER=r2
BACKUP_STORAGE_BUCKET=company-backups
BACKUP_STORAGE_PREFIX=inventory/prod
R2_ENDPOINT_URL=https://<account-id>.r2.cloudflarestorage.com
AWS_ACCESS_KEY_ID=<access-key>
AWS_SECRET_ACCESS_KEY=<secret-key>
BACKUP_COMPOSE_DIRECTORY=/opt/inventory
BACKUP_COMPOSE_FILE=docker-compose.yml
```

`BACKUP_COMPOSE_DIRECTORY` defaults to the current directory and `BACKUP_COMPOSE_FILE` defaults to `docker-compose.yml`. Set `BACKUP_COMPOSE_ENV_FILE` if Compose needs a non-default interpolation env file. The matching `_SERVICE` value is the Compose service name, not `container_name`. In Compose mode, `_HOST` is optional; the client tools use the local PostgreSQL socket inside that service.

The configured environment variable (default `POSTGRES_PASSWORD`) must be present in the database service. Compose mode does not need the database password in the VM environment. The VM account running the binary needs Docker Compose access; Docker daemon access grants host-level control, so use a dedicated trusted deployment account. Do not mount `/var/run/docker.sock` into the backup-sys image. Schedule `backup-sys backup --frequency daily` from cron/systemd, or run `backup-sys schedule` as a service.

After downloading and extracting the Linux binary artifact from its tagged workflow run, install it with:

```sh
tar -xzf backup-sys-linux-amd64.tar.gz
sudo install -m 0755 backup-sys /usr/local/bin/backup-sys
```

Use `backup-sys-linux-arm64.tar.gz` on ARM64 VMs.

## TCP mode

The default `BACKUP_EXECUTION_MODE=tcp` connects to PostgreSQL over TCP and requires `pg_dump`, `dropdb`, `createdb`, and `pg_restore` on the same host. Use this mode in the Docker image or when PostgreSQL tools are already installed on the VM.

## Configuration

Set `BACKUP_DATABASES` to comma-separated database aliases. In TCP mode, set `BACKUP_DB_<ALIAS>_HOST`, `PORT`, `NAME`, `USER`, and `PASSWORD`. In Compose mode, set `SERVICE`, `NAME`, and `USER`; `_PASSWORD_ENV` defaults to `POSTGRES_PASSWORD` and identifies the password variable inside the database container. Aliases are case-insensitive and may contain letters, digits, `_`, and `-`.

Storage settings: `BACKUP_STORAGE_PROVIDER` (`s3`, `r2`, `gcs`, or empty for local only), `BACKUP_STORAGE_BUCKET`, optional `BACKUP_STORAGE_PREFIX`, and `BACKUP_DIR` (default `/backups`). S3/R2 use `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`; R2 also requires the account S3 endpoint in `R2_ENDPOINT_URL`. GCS uses Application Default Credentials.

The default command schedules an immediate backup and then repeats at `BACKUP_INTERVAL_SECONDS`. Set `BACKUP_RUN_ON_START=false` to skip the initial scheduled run. One-off commands:

```sh
docker run --rm --env-file .env -v postgres_backups:/backups ghcr.io/steven-mendoza-z/backup-sys:1.0.0 backup --frequency manual
docker run --rm --env-file .env -v postgres_backups:/backups ghcr.io/steven-mendoza-z/backup-sys:1.0.0 restore --frequency manual --timestamp 20261020T120000Z
docker run --rm --env-file .env -v postgres_backups:/backups ghcr.io/steven-mendoza-z/backup-sys:1.0.0 restore --frequency pre-deploy --backup-name inventory-app-20261020T120000Z.dump --database app
```

Restore replaces the selected target database. Select a known set by timestamp or dump filename, or choose the newest complete set for the requested frequency:

```sh
backup-sys restore --frequency pre-deploy --timestamp 20261020T120000Z
backup-sys restore --frequency pre-deploy --backup-name inventory-app-20261020T120000Z.dump --database app
backup-sys restore --frequency pre-deploy --latest
backup-sys restore --frequency daily --latest --database app
```

Exactly one of `--timestamp`, `--backup-name`, or `--latest` is required. A backup filename must be the basename in the format `<project>-<database>-<timestamp>.dump` generated by backup-sys, with no path. Its project and database alias are validated against the configuration and selected databases. The extracted timestamp selects that backup set; without `--database`, it restores all configured databases from the same timestamp, while `--database` can restrict the restore. `--latest` chooses the newest timestamp complete for the selected databases, combining local and remote objects. Remote latest discovery requires list permission under the project's backup prefix as well as download permission. If a backup set is incomplete, latest skips it and chooses the next newest complete set; it fails without restoring anything when no complete set exists. Dumps use PostgreSQL custom format; local retention defaults to three backup sets per database and frequency. Remote object retention is managed by the bucket's lifecycle policy.

## Docker Compose

Give the service network access to the configured database hosts and mount persistent storage at `/backups`:

```yaml
services:
  postgres-backup:
    image: ghcr.io/steven-mendoza-z/backup-sys:1.0.0
    environment:
      BACKUP_PROJECT: inventory
      BACKUP_DATABASES: app
      BACKUP_DB_APP_HOST: postgres
      BACKUP_DB_APP_PORT: 5432
      BACKUP_DB_APP_NAME: inventory
      BACKUP_DB_APP_USER: ${POSTGRES_USER}
      BACKUP_DB_APP_PASSWORD: ${POSTGRES_PASSWORD}
      BACKUP_STORAGE_PROVIDER: r2
      BACKUP_STORAGE_BUCKET: company-backups
      BACKUP_STORAGE_PREFIX: inventory/prod
      R2_ENDPOINT_URL: ${R2_ENDPOINT_URL}
      AWS_ACCESS_KEY_ID: ${AWS_ACCESS_KEY_ID}
      AWS_SECRET_ACCESS_KEY: ${AWS_SECRET_ACCESS_KEY}
    volumes:
      - postgres_backups:/backups
    restart: unless-stopped
```

Keep credentials in a secrets manager or protected deployment environment. Grant the storage identity only required object access and periodically verify restores in a non-production environment.

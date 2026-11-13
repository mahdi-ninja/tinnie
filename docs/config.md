# Config reference

[Back to the README](../README.md)

Examples use the WordPress stack from the [Quick start](../README.md#quick-start) where a type fits it.

| Key | Required | Description |
|---|---|---|
| `name` | | Identifies this stack's snapshots in the repository. Lowercase letters, digits, `-` and `_`, starting with a letter or digit. Default `tinnie` |
| `schedule` | yes | Cron expression. See [Schedule](#schedule) |
| `timezone` | | IANA time zone such as `Europe/London`. Default `UTC` |
| `repository.url` | yes | Any restic repository: a path such as `/backups/restic`, or `s3:`, `rclone:`, `rest:` and so on. See [Repository](../README.md#repository) |
| `repository.password` | yes | Encryption password for the repository |
| `repository.env` | | Environment variables for restic, for example `AWS_ACCESS_KEY_ID` for an `s3:` repository. restic gets only these (plus `PATH`, `HOME`, `TZ` and `TMPDIR`), not the container's environment |
| `repository.keep` | | Retention: `within` (such as `24h`, `30d` or `1y6m`), `last`, `hourly`, `daily`, `weekly`, `monthly`, `yearly`. Without it, every snapshot is kept |
| `stop` | | Compose services to stop while backing up and restoring. See [Stopping services](docker.md#stopping-services) |
| `notify.urls` | | A [shoutrrr](https://github.com/nicholas-fedor/shoutrrr/blob/main/docs/services/overview.md) URL, or a list of them. See [Notifications](notifications.md) |
| `notify.level` | | `error` (default) notifies only when a backup fails; `info` also when it succeeds |
| `sources` | yes | List of sources, each with a unique `name` (lowercase letters, digits, `-` and `_`, starting with a letter or digit) and a `type`: `postgres`, `mysql`, `s3`, `volume` or `command` |

Unknown keys are refused, so a typo fails at start instead of being ignored.

## Restore policy

Every source is backed up on every run. `restore_policy`, a key any source can have, sets when `tinnie restore` replaces it:

| `restore_policy` | `tinnie restore` | `tinnie restore latest <source>` |
|---|---|---|
| `auto` (default) | restored | restored |
| `manual` | skipped | restored |
| `never` | skipped | refused |

Use `manual` for a source you only want replaced on purpose, such as certificates, and `never` for one that is backed up as a safety copy and must not be overwritten, such as a bucket another system owns.

```yaml
      sources:
        - name: certs
          type: volume
          path: /sources/certs
          restore_policy: manual
```

A skipped source is listed before the restore asks you to confirm, and the console summary, logs and `tinnie check` mark it with `(manual restore)` or `(no restore)`. Its data is in every snapshot either way; for a `never` source, get it out with restic by hand.

## `type: postgres`

Backed up with `pg_dump`, restored with `pg_restore`.

| Key | Required | Description |
|---|---|---|
| `host` | yes | |
| `port` | | Default `5432` |
| `database` | yes | |
| `user` | yes | |
| `password` | | |
| `sslmode` | | Passed as `PGSSLMODE` |

```yaml
      sources:
        - name: database
          type: postgres
          host: postgres
          database: app
          user: app
          password: ${DB_PASSWORD}
```

The dump is in custom format, made with the client that matches the server: Postgres 16, 17 and 18 are included, older servers use the 16 client, and a newer server fails with an error. A restore drops every non-system schema in the database and then restores the dump (without owners), so the database ends up exactly as it was in the snapshot.

## `type: mysql`

Backed up with `mariadb-dump`, restored with `mariadb`. For MySQL and MariaDB; MySQL 8's default `caching_sha2_password` login works too.

| Key | Required | Description |
|---|---|---|
| `host` | yes | |
| `port` | | Default `3306` |
| `database` | yes | |
| `user` | yes | |
| `password` | | |

```yaml
      sources:
        - name: database
          type: mysql
          host: db
          database: wp
          user: wp
          password: ${DB_PASSWORD}
```

The dump uses `--single-transaction` and includes routines, triggers and events. The user needs full rights on the database, as the user an app connects as usually has (the `MARIADB_USER` or `MYSQL_USER` of the official images does). The server's TLS certificate is not verified, since stack databases have none to verify. A restore drops every table, view, routine and event in the database and then loads the dump, so the database ends up exactly as it was in the snapshot. The database itself and its users and grants are left alone.

## `type: s3`

Backed up and restored with `rclone sync`.

| Key | Required | Description |
|---|---|---|
| `endpoint` | | Leave out for AWS. For SeaweedFS, MinIO or R2, the S3 endpoint URL |
| `region` | | |
| `bucket` | yes | |
| `prefix` | | Only back up objects under this path |
| `access_key` | yes | |
| `secret_key` | yes | |
| `path_style` | | `true` for SeaweedFS and MinIO |
| `provider` | | rclone S3 provider. Default `Other`; use `Cloudflare` for R2 or `AWS` for AWS |

```yaml
      sources:
        - name: uploads
          type: s3
          endpoint: https://<account-id>.r2.cloudflarestorage.com
          provider: Cloudflare
          bucket: app-uploads
          access_key: ${S3_ACCESS_KEY}
          secret_key: ${S3_SECRET_KEY}
```

A restore syncs the snapshot back, deleting objects that are not in it.

## `type: volume`

Backed up and restored by restic, reading and writing the mounted volume in place.

| Key | Required | Description |
|---|---|---|
| `path` | yes | Where the volume is mounted in the tinnie container, such as `/sources/files`. `/` is refused |
| `exclude` | | List of patterns, relative to `path`, to leave out, such as `wp-content/cache` or `**/*.log`. They must stay inside `path`, and `.` (everything) is refused |

Mount each volume read-write at `/sources/<name>` in the tinnie service, using the same volume as the app (here `wordpress_files`):

```yaml
services:
  wordpress:
    image: wordpress:7
    volumes:
      - wordpress_files:/var/www/html

  backup:
    image: ghcr.io/mahdi-ninja/tinnie:1
    volumes:
      - wordpress_files:/sources/files
      ...
```

```yaml
      sources:
        - name: files
          type: volume
          path: /sources/files
          exclude:
            - wp-content/cache
```

Mount the volume itself at `path`: a symlink there is refused, since restic would back up the link instead of the files. `path` must not contain, or be inside, `/var/lib/tinnie`, a local repository or another volume source's `path`.

Nothing is copied first, and owners, permissions and symlinks are kept. A restore writes the snapshot straight back into the mount and deletes files added since. Excluded paths are not backed up and a restore leaves them as they are.

## `type: command`

Backed up and restored with commands you choose, run in another service of the same stack through the Docker Engine API. tinnie saves what the backup command prints. Use it for apps that have their own export, such as `gitea dump`, or for a database tinnie has no client for.

| Key | Required | Description |
|---|---|---|
| `service` | yes | The Compose service to run the command in, such as `gitea` |
| `user` | | User to run the commands as, such as `git`. Default: the container's user |
| `backup` | yes | Command whose output is backed up |
| `restore` | | Command that gets the backed-up output on its input and puts it back |

For a Gitea service in the same stack:

```yaml
      sources:
        - name: gitea
          type: command
          service: gitea
          user: git
          backup: gitea dump --type tar.gz -f /dev/stdout
          restore: cat > /data/restore/gitea-dump.tar.gz
```

Both commands run with `/bin/sh -c` inside the service's running container, as `docker exec` would, so pipes and redirects work and the container needs a shell. A command that exits with an error fails the backup or restore, and the last lines it printed to stderr are in the error. The service must have exactly one running container.

The commands themselves are never shown, since a command may hold a password: the console summary, logs and `tinnie check` show the source as `service gitea`, and errors say "the backup command" or "the restore command".

If tinnie is stopped or interrupted while a command runs (Ctrl+C, a closed console, a redeploy), the command keeps running in its container until it finishes on its own: Docker has no way to stop an exec. An interrupted restore command may then have read only part of the saved output, so run the restore again.

Without `restore`, the source's [restore policy](#restore-policy) is `never`: `tinnie restore` skips it and restores the others, and `tinnie restore latest gitea` fails. The saved output is in the snapshot to restore by hand. Setting `restore_policy` to `auto` or `manual` without a `restore` command is refused.

The service cannot be under `stop.backup`, nor under `stop.restore` unless the source is never restored (see [Stopping services](docker.md#stopping-services)). This needs [Docker access](docker.md#docker-access).

## Schedule

`schedule` is a standard five-field cron expression (`minute hour day month weekday`) or a shortcut such as `@daily`, evaluated in `timezone` (default UTC).

When the container starts, tinnie checks the latest snapshot. If a scheduled backup was missed while it was down, or there is no snapshot yet, it backs up straight away; otherwise it waits for the next scheduled time. A scheduled backup that fails is retried twice, a minute apart, before it is reported as failed. A backup that could not start a [stopped service](docker.md#stopping-services) again is reported as failed straight away instead.

## Volumes

| Path | What it holds |
|---|---|
| `/var/lib/tinnie` | Such as `tinnie_state`. Working copies of each source, the restic cache, the last backup status and the list of services tinnie has stopped. Keep it on a volume so each backup only copies what changed, and stopped services are started again after a crash |
| Repository path | Wherever `repository.url` points, if it is local, such as `backup_repo` at `/backups` |
| `/sources/<name>` | Volumes backed up by `type: volume` sources, mounted read-write, such as `wordpress_files` at `/sources/files` |
| `/var/run/docker.sock` | Only for `stop` and `type: command`. See [Docker access](docker.md#docker-access) |

## Health

The container is healthy when the last backup succeeded and no more than one scheduled backup has been missed since. Portainer shows it as unhealthy otherwise. The first 12 hours after the container starts are not counted, so a first large backup has time to finish.

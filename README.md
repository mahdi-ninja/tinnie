# tinnie

A small backup container for Docker Compose and Portainer stacks. It backs up Postgres and MySQL/MariaDB databases, S3 buckets, Docker volumes and the output of commands run in your other containers on a schedule into an encrypted, deduplicated [restic](https://restic.net) repository, and restores them with one command.

- One config, written inline in your stack
- Snapshots hold every source together, so a database and its files always match
- Stops the services you list while it backs up or restores, and starts them again
- Retention rules, a lock so backups and restores never overlap, retries, a healthcheck, and notifications to ntfy, email, Telegram, Discord and many more
- Open a console in Portainer and it shows what it backs up and the commands you can run

## How it works

tinnie schedules the work and hands each job to a tool built for it:

| Job | Done with |
|---|---|
| Repository | [restic](https://restic.net): encryption, deduplication, retention and every storage backend restic supports |
| Postgres | `pg_dump` and `pg_restore`, with the client that matches the server (16, 17 and 18 are included) |
| MySQL and MariaDB | `mariadb-dump` and `mariadb` from the MariaDB 11.4 client. MySQL 8 is supported |
| S3 buckets | `rclone sync` |
| Volumes | restic reads the mounted volume directly and restores it in place |
| `stop` and `type: command` | The Docker Engine API over the socket. There is no docker CLI in the image |
| Notifications | [shoutrrr](https://github.com/nicholas-fedor/shoutrrr/blob/main/docs/services/overview.md) |

## Quick start

Add tinnie to a stack. For a WordPress site:

```yaml
services:
  db:
    image: mariadb:11
    restart: unless-stopped
    environment:
      MARIADB_DATABASE: wp
      MARIADB_USER: wp
      MARIADB_PASSWORD: ${DB_PASSWORD}
      MARIADB_RANDOM_ROOT_PASSWORD: "1"
    volumes:
      - db_data:/var/lib/mysql

  wordpress:
    image: wordpress:7
    restart: unless-stopped
    environment:
      WORDPRESS_DB_HOST: db
      WORDPRESS_DB_NAME: wp
      WORDPRESS_DB_USER: wp
      WORDPRESS_DB_PASSWORD: ${DB_PASSWORD}
    volumes:
      - wordpress_files:/var/www/html

  backup:
    image: ghcr.io/mahdi-ninja/tinnie:1
    restart: unless-stopped
    volumes:
      - backup_repo:/backups                         # the restic repository
      - tinnie_state:/var/lib/tinnie                 # working copies, cache and status
      - wordpress_files:/sources/files               # a source; read-write so restores can write back
      - /var/run/docker.sock:/var/run/docker.sock    # optional: only for `stop` and `type: command`
    configs:
      - source: tinnie
        target: /config/tinnie.yml

configs:
  tinnie:
    content: |
      name: mysite
      schedule: "0 */6 * * *"
      timezone: Australia/Sydney

      repository:
        url: /backups/restic
        password: ${BACKUP_PASSWORD}
        keep: { within: 24h, daily: 7, weekly: 4, monthly: 6 }

      stop: [wordpress]                              # services to stop while backing up and restoring

      notify:
        urls: ntfy://ntfy.sh/my-private-topic

      sources:
        - name: database
          type: mysql
          host: db
          database: wp
          user: wp
          password: ${DB_PASSWORD}

        - name: files
          type: volume
          path: /sources/files
          exclude: [wp-content/cache]

volumes:
  db_data:
  wordpress_files:
  backup_repo:
  tinnie_state:
```

This keeps the repository on the same host; to send it to R2, B2 or SFTP, swap `repository` for one from [Repository](#repository).

Set `BACKUP_PASSWORD` and `DB_PASSWORD` as stack environment variables. Compose fills in every `${...}` when you deploy, so secrets never sit in the stack text. Keep `BACKUP_PASSWORD` somewhere safe: without it the backups cannot be read.

On first start there is no snapshot yet, so tinnie backs up straight away, stopping WordPress for the length of that backup.

`stop` stops WordPress while the database and files are backed up, so they match, and starts it again afterwards. It is the only reason the Docker socket is mounted (Compose stacks only; Portainer Swarm stacks are not supported), and that socket is root access to the host (see [Docker access](docs/docker.md#docker-access)); leave both out if a backup taken while WordPress runs is good enough.

`notify` is optional; see [Notifications](docs/notifications.md) for email, Telegram, Discord and more.

Embedding a file with `configs: content:` needs Compose 2.23.1 or newer. On older setups, put the same YAML in a `TINNIE_CONFIG` environment variable instead:

```yaml
    environment:
      TINNIE_CONFIG: |
        name: mysite
        ...
```

tinnie reads `TINNIE_CONFIG` first, then the file at `TINNIE_CONFIG_FILE` (default `/config/tinnie.yml`).

## Commands

Open a console on the container (`/bin/sh` or `/bin/bash`; in Portainer: Containers → the `backup` container, such as `mysite-backup-1` → Console → `/bin/sh`) to see a summary and these commands:

| Command | What it does |
|---|---|
| `tinnie snapshots` | List snapshots |
| `tinnie backup` | Back up every source now |
| `tinnie restore [snapshot [source...]]` | Replace sources with a snapshot. Defaults to `latest` and every source, except those a [restore policy](docs/config.md#restore-policy) leaves out. Asks you to type `restore`; add `--yes` (or `-y`) to skip that in scripts |
| `tinnie check` | Validate the config and test every connection |
| `tinnie help` | Show the summary and commands |

Examples:

```sh
tinnie restore                 # everything, from the latest snapshot
tinnie restore 4f2a91c3        # everything, from a specific snapshot
tinnie restore latest database # only the database source
```

A restore makes each source match the snapshot exactly: anything added since is deleted. Stop the apps that write to those sources before restoring, and start them again afterwards, or list them under [`stop`](docs/docker.md#stopping-services) and tinnie does it for you. `tinnie restore` shows what it will replace and asks first:

```
This replaces the following with snapshot latest:
  database     mysql wp@db:3306/wp
  files        volume /sources/files
Everything added since that snapshot is deleted. tinnie stops wordpress while it restores and starts it again afterwards.
Type restore to continue:
```

A source with [`restore_policy: manual`](docs/config.md#restore-policy) is left out unless you name it, and one with `never` is always left out; the list above then ends with a line such as `Skipping certs: its restore_policy is manual, so it is only restored when named.`

`tinnie check` connects to every source and the repository without changing anything (notification URLs are checked when the config is read). When the config has `stop` or a `type: command` source, it also checks that it can reach Docker and that each service under `stop` has a container:

```
Config is valid. Checking connections:
  ✓ Docker at unix:///var/run/docker.sock
  ✓ stop wordpress
  ✓ database (mysql wp@db:3306/wp)
  ✓ files (volume /sources/files)
  ✓ repository /backups/restic
```

## Repository

A repository on the same host as the stack is not a backup if that host dies. Keep it somewhere else, or copy it somewhere else.

`repository.url` takes any restic repository. The Quick start uses a local path on a volume:

```yaml
      repository:
        url: /backups/restic
        password: ${BACKUP_PASSWORD}
```

For Cloudflare R2 the URL is `s3:https://<account-id>.r2.cloudflarestorage.com/<bucket>/<folder>`. Create the bucket and an R2 API token with Object Read & Write, add its keys as the stack variables `S3_ACCESS_KEY` and `S3_SECRET_KEY`, and use:

```yaml
      repository:
        url: s3:https://<account-id>.r2.cloudflarestorage.com/backups/mysite
        password: ${BACKUP_PASSWORD}
        env:
          AWS_ACCESS_KEY_ID: ${S3_ACCESS_KEY}
          AWS_SECRET_ACCESS_KEY: ${S3_SECRET_KEY}
```

Then remove the `backup_repo:/backups` mount from the `backup` service and the `backup_repo:` entry under `volumes:`; the `backup_repo` volume is then no longer needed.

restic gets only the variables under `repository.env` (plus `PATH`, `HOME`, `TZ` and `TMPDIR`), not the container's environment.

tinnie creates the repository on the first backup. Backblaze B2 and SFTP are in [docs/repository.md](docs/repository.md), and restic's [repository docs](https://restic.readthedocs.io/en/stable/030_preparing_a_new_repo.html) cover every other backend.

### Your own backup server

[tinnie-server](docs/server.md) is a companion image for a host of your own: restic's rest-server in append-only mode, with a private repository per stack and the retention run on the server. A stack can add snapshots and restore from them, but cannot delete them, so a compromised stack cannot destroy its own backups. Each stack points at its user there, with no `keep` block:

```yaml
      repository:
        url: rest:https://mysite:${REST_PASSWORD}@backups.example.com/mysite
        password: ${BACKUP_PASSWORD}
```

## Config

The config is YAML, written inline in the stack. The full reference is in [docs/config.md](docs/config.md).

| Key | What it sets |
|---|---|
| `name` | Identifies this stack's snapshots in the repository. Default `tinnie` |
| `schedule` | Cron expression for backups (required). See [Schedule](docs/config.md#schedule) |
| `timezone` | Time zone for `schedule`. Default `UTC` |
| `repository` | `url`, `password`, `env` and `keep` (retention). See [Repository](#repository) |
| `stop` | Compose services to stop while backing up and restoring. See [Stopping services](docs/docker.md#stopping-services) |
| `notify` | Where to send notifications, and for what. See [docs/notifications.md](docs/notifications.md) |
| `sources` | What to back up (required), each with a unique `name` and a `type` |

Source types:

- [`postgres`](docs/config.md#type-postgres): a Postgres database, with `pg_dump`
- [`mysql`](docs/config.md#type-mysql): a MySQL or MariaDB database, with `mariadb-dump`
- [`s3`](docs/config.md#type-s3): an S3 bucket or a prefix in it, with `rclone`
- [`volume`](docs/config.md#type-volume): a Docker volume mounted into tinnie, read in place
- [`command`](docs/config.md#type-command): the output of a command run in another service

Any source can set [`restore_policy`](docs/config.md#restore-policy) to `manual` (restored only when named) or `never` (backed up, not restored).

## Development

```sh
go test ./...
test/e2e/run.sh       # builds the image and runs a full stack in Docker
test/e2e/server.sh    # builds tinnie-server and a stack that backs up to it
```

More in [docs/development.md](docs/development.md).

Docs: [Config](docs/config.md) · [Repositories](docs/repository.md) · [tinnie-server](docs/server.md) · [Stopping services and Docker access](docs/docker.md) · [Notifications](docs/notifications.md) · [Private networks](docs/network.md) · [Development](docs/development.md)

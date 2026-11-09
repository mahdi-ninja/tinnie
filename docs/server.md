# tinnie-server

[Back to the README](../README.md)

tinnie-server is the backup server for tinnie stacks: one container on another host that every stack sends its snapshots to. It runs restic's [rest-server](https://github.com/restic/rest-server) with two rules that are always on:

- **Append-only.** A stack can add snapshots and read them back, but cannot delete or overwrite anything. A compromised stack cannot destroy its own backups.
- **Private repositories.** Each stack has a user that only reaches its own repository.

Because stacks cannot delete, tinnie-server runs the retention itself, on a schedule, with each stack's backup password. The tinnie configs on the stacks then have no `keep` block.

## Stack

On the server host, for Portainer or Komodo:

```yaml
services:
  backups:
    image: ghcr.io/mahdi-ninja/tinnie-server:1
    restart: unless-stopped
    volumes:
      - repos:/data
    configs:
      - source: server
        target: /config/tinnie-server.yml

configs:
  server:
    content: |
      url: https://backups.example.com
      timezone: Australia/Sydney
      max_size: 180G

      retention:
        schedule: "0 3 * * *"
        keep: { within: 24h, daily: 7, weekly: 4, monthly: 6 }

      notify:
        urls: ntfy://ntfy.sh/my-private-topic

      stacks:
        - name: mysite
          password: ${MYSITE_REST_PASSWORD}
          backup_password: ${MYSITE_BACKUP_PASSWORD}
        - name: shop
          password: ${SHOP_REST_PASSWORD}
          backup_password: ${SHOP_BACKUP_PASSWORD}

volumes:
  repos:
```

rest-server listens on port 8000 inside the container. Put a reverse proxy with TLS in front of it, or a Cloudflare Tunnel pointed at `http://backups:8000` (see [Cloudflare Tunnel](#cloudflare-tunnel)); never expose port 8000 itself.

Each stack needs two passwords. Generate them with `openssl rand -hex 24`:

| | Lives on the server as | Lives on the stack as | Protects against |
|---|---|---|---|
| `password` | a bcrypt hash | the user info in `repository.url` | anyone reaching the server |
| `backup_password` | plaintext, for retention | `repository.password` | anyone holding the repository files, including the server host and whatever terminates TLS |

## The stack side

A stack points tinnie at its user, with no `keep` block:

```yaml
      repository:
        url: rest:https://mysite:${REST_PASSWORD}@backups.example.com/mysite
        password: ${BACKUP_PASSWORD}
```

`REST_PASSWORD` is the stack's `password` on the server, and `BACKUP_PASSWORD` its `backup_password`. The user name and the path after the host must both be the stack's `name`. `tinnie-server help` prints this URL for every stack. The stack creates its repository on the first backup, so nothing is set up on the server beforehand.

## Config

| Key | What it sets |
|---|---|
| `url` | The public base URL, used only to print each stack's repository URL in `help`. Optional |
| `timezone` | Time zone for `retention.schedule`. Default `UTC` |
| `max_size` | Hard cap on everything under `/data`, such as `180G` (units are powers of 1024). Uploads past it fail, so one flooded stack cannot fill the disk. Optional |
| `retention.schedule` | Cron expression for retention. Default `0 3 * * *` (daily at 03:00) |
| `retention.keep` | Default keep rules, the same keys as tinnie's `repository.keep` |
| `notify` | Where to send notifications, as in [Notifications](notifications.md). Retention failures are always sent, successes with `level: info` |
| `stacks` | One entry per tinnie stack |

Each stack has:

| Key | What it sets |
|---|---|
| `name` | The user name and repository path. Lowercase letters, digits, `-` and `_` |
| `password` | What the stack puts in its repository URL. Required; no `:`, `@`, `/` or spaces |
| `backup_password` | The stack's `repository.password`, so retention can open the repository. Without it the stack's snapshots are kept forever and `help` cannot show them |
| `keep` | Keep rules for this stack instead of `retention.keep` |

Pick a retention time when no stack is backing up: pruning takes an exclusive lock on the repository, and a backup that arrives during it has to retry.

## Commands

Open a console on the container (in Portainer: Containers → the container → Console → `/bin/sh`) and it shows every stack, its repository URL, size, last snapshot and retention rules, plus:

| Command | What it does |
|---|---|
| `tinnie-server forget [stack...]` | Run retention now, for every stack or the ones named |
| `tinnie-server snapshots <stack>` | List a stack's snapshots |
| `tinnie-server check` | Validate the config, that rest-server answers, and that every `backup_password` opens its repository |
| `tinnie-server restic <stack> <args...>` | Run any restic command on a stack's repository, for the maintenance clients cannot do over the append-only API: `key passwd`, `repair index`, `check --read-data` and so on |
| `tinnie-server help` | Show the summary and commands |

## Adding, changing and removing stacks

Edit the `stacks` list and redeploy. The password file is rewritten from the config on every start, so a new stack has access right away, a changed `password` takes effect, and a stack removed from the config loses access. Its repository stays under `/data/<name>` until you delete it.

If a stack's `backup_password` does not open its repository, `check` and the start-up log say so. That means the stack and the server disagree about `BACKUP_PASSWORD`.

## Health

The container is unhealthy when rest-server stops answering, when the last retention run failed, or when more than one scheduled run has been missed.

## Cloudflare Tunnel

To avoid open ports, add cloudflared to the same stack and point a public hostname at `http://backups:8000` in Zero Trust:

```yaml
  cloudflared:
    image: cloudflare/cloudflared:latest
    restart: unless-stopped
    command: tunnel --no-autoupdate run
    environment:
      TUNNEL_TOKEN: ${TUNNEL_TOKEN}
```

Cloudflare rejects request bodies over 100 MB on the free plan. restic's pack files are about 16 MiB by default, so this only matters if a stack raises restic's pack size.

## What append-only does not cover

Someone with root on the server host, or admin on its Portainer, has both the repositories and the `backup_password`s in the container's config, and can read and destroy every backup. The passwords are never written to `/data`, so a copy of the volume alone is still unreadable. The server host itself still needs a backup of `/data` if it is the only copy.

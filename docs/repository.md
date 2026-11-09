# Repositories

[Back to the README](../README.md)

The README's [Repository](../README.md#repository) section covers a local path and Cloudflare R2. Two more:

## Backblaze B2

Through B2's S3-compatible API, at `s3:https://s3.<region>.backblazeb2.com/<bucket>/<folder>` (for example `s3.us-west-004`). Create the bucket and an application key for it, and add the key ID and key as the stack variables `S3_ACCESS_KEY` and `S3_SECRET_KEY`:

```yaml
      repository:
        url: s3:https://s3.us-west-004.backblazeb2.com/backups/mysite
        password: ${BACKUP_PASSWORD}
        env:
          AWS_ACCESS_KEY_ID: ${S3_ACCESS_KEY}
          AWS_SECRET_ACCESS_KEY: ${S3_SECRET_KEY}
```

As with R2, remove the `backup_repo:/backups` mount and the `backup_repo:` entry under `volumes:`.

## SFTP

restic's `sftp:` backend, at `sftp:user@host:/path`. restic runs `ssh` as root in the container, so mount a private key without a passphrase at `/root/.ssh/id_ed25519` and the server's host keys at `/root/.ssh/known_hosts`:

```yaml
  backup:
    image: ghcr.io/mahdi-ninja/tinnie:1
    volumes:
      - ./backup_key:/root/.ssh/id_ed25519:ro
      - ./known_hosts:/root/.ssh/known_hosts:ro
      ...
```

```yaml
      repository:
        url: sftp:backup@backup.example.com:/srv/restic/mysite
        password: ${BACKUP_PASSWORD}
```

Make `known_hosts` with `ssh-keyscan backup.example.com > known_hosts` and keep every key it prints; with only some of them, ssh tries to add the rest to the read-only file and logs an error. ssh refuses a private key that others can read, so `chmod 600` it. For another port, use `sftp://backup@backup.example.com:2222//srv/restic/mysite`.

## tinnie-server

restic's `rest:` backend, on a host of your own, with append-only access and a private repository per stack. See [tinnie-server](server.md):

```yaml
      repository:
        url: rest:https://mysite:${REST_PASSWORD}@backups.example.com/mysite
        password: ${BACKUP_PASSWORD}
```

Leave out `keep`: the server runs retention, and a stack is not allowed to delete snapshots.

## Everything else

restic's [repository docs](https://restic.readthedocs.io/en/stable/030_preparing_a_new_repo.html) cover the rest: any other REST server, Azure, Google Cloud Storage, any rclone remote and more. Put the variables a backend needs under `repository.env`.

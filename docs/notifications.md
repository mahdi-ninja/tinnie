# Notifications

[Back to the README](../README.md)

tinnie sends notifications with [shoutrrr](https://github.com/nicholas-fedor/shoutrrr/blob/main/docs/services/overview.md), so one URL per service is all it needs. The [Quick start](../README.md#quick-start) uses ntfy; some other examples:

```yaml
      notify:
        urls:
          - ntfy://ntfy.sh/my-private-topic
          - smtp://user:${SMTP_PASSWORD}@smtp.example.com:587/?from=backup@example.com&to=me@example.com
          - telegram://${TELEGRAM_TOKEN}@telegram?chats=@my-channel
          - discord://${DISCORD_TOKEN}@${DISCORD_WEBHOOK_ID}
          - generic+https://example.com/hooks/backup
        level: info
```

`generic+https://` POSTs to any other webhook; add `?template=json` to get `{"title": ..., "message": ...}` as JSON. The [service docs](https://github.com/nicholas-fedor/shoutrrr/blob/main/docs/services/overview.md) list every service and its options.

By default (`level: error`) tinnie only notifies when a backup fails. With `level: info` it also notifies when one succeeds. Restores are not notified. A failure looks like this:

```
tinnie mysite: backup failed

The backup of mysite failed.
Started: 2026-11-01T06:00:00Z
Duration: 12s
Error: database: mariadb-dump: exit status 2: mariadb-dump: Got error: 2005: "Unknown server host 'db' (-2)" when trying to connect
```

A success:

```
tinnie mysite: backup 4f2a91c3 saved

The backup of mysite was saved as snapshot 4f2a91c3.
Started: 2026-11-01T06:00:00Z
Duration: 41s
```

Services stopped by an earlier backup or restore that tinnie cannot start again when the container starts are reported the same way, as a failed backup.

Every URL is checked when tinnie starts and by `tinnie check`, so a typo shows up straight away instead of when a backup fails. Each notification gets 15 seconds; one that cannot be sent is logged and does not fail the backup.

A self-hosted ntfy over plain http needs `?scheme=http`, as in `ntfy://ntfy.example.lan/my-topic?scheme=http`; shoutrrr uses https by default.

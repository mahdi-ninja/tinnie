# Stopping services and Docker access

[Back to the README](../README.md)

## Stopping services

Some apps keep data in memory or write while a backup reads, so their files are only consistent while the app is stopped. List those services under `stop` and tinnie stops them for the backup and the restore. In the [Quick start](../README.md#quick-start) that is WordPress:

```yaml
      stop: [wordpress]
```

That is short for the same list for both. For example, if your site runs nginx and php-fpm as separate services, you can stop different services for each:

```yaml
      stop:
        backup: [php-fpm]
        restore: [php-fpm, nginx]
```

The names are Compose service names in the same stack as tinnie. For a backup, tinnie stops every running container of each service, in the order listed, just before the first source is backed up (each gets 30 seconds to stop before Docker kills it), and starts them again, in reverse order, as soon as the snapshot is saved. For a restore, it stops them before anything is replaced and starts them again when the restore has finished. Only the containers tinnie stopped are started again, so a service that was already stopped stays stopped.

The services are started again even when the backup or restore fails or is interrupted. Before stopping a service, tinnie writes it to `/var/lib/tinnie/stopped.json`, and removes it once it is running again. If tinnie is killed before it can start a service, or the start fails, the next time the container starts, or the next backup or restore, starts it first. If a service cannot be started, the backup or restore is reported as failed (and notified) and is not retried, so you hear about it; every later backup fails the same way until the service runs again, whether tinnie or you started it.

tinnie never stops its own container: a service under `stop` that turns out to be tinnie is skipped with a log line, and `tinnie check` reports it as a failure.

A `type: command` source needs its service running, so tinnie refuses a config that lists that service under `stop.backup`, or under `stop.restore` when the source has a `restore` command. Likewise a `postgres` or `mysql` source whose `host` is a service under `stop.backup` or `stop.restore` is refused, since the database would be down when tinnie connects. That is why the Quick start stops `wordpress` and not `db`.

This needs [Docker access](#docker-access) and only works in Compose stacks. Portainer stacks on a standalone Docker environment are Compose stacks; Swarm stacks are not supported.

## Docker access

`stop` and `type: command` talk to the Docker Engine API; the image has no docker CLI. Nothing else uses Docker: without them tinnie never touches it and needs no socket. With them, mount the socket into the tinnie service:

```yaml
  backup:
    image: ghcr.io/mahdi-ninja/tinnie:1
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      ...
```

Be clear about what this means: access to the Docker socket is root access to the host. Anything that can talk to it can start a container that mounts the host's disk. Only add it if you need `stop` or `type: command`.

To narrow that down, put a socket proxy such as [docker-socket-proxy](https://github.com/Tecnativa/docker-socket-proxy) in between and point `DOCKER_HOST` at it, for example `DOCKER_HOST: tcp://docker-proxy:2375`. tinnie needs the proxy to allow `CONTAINERS`, `POST` and `EXEC`. `DOCKER_HOST` also takes another socket path, such as `unix:///run/user/1000/docker.sock` for rootless Docker; TLS is not supported.

tinnie finds its own container (by its hostname, or else by its container id) and reads the Compose project from it. Services are looked up in that project only, so tinnie must run as a service of the same stack, on the same Docker host.

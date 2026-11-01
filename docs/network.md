# Reaching sources through a private network

[Back to the README](../README.md)

If the sources live on another server, run tinnie on the network that reaches them. For example, with a Cloudflare Mesh or Tailscale container named `mesh` in the stack, give the `backup` service its network:

```yaml
  backup:
    image: ghcr.io/mahdi-ninja/tinnie:1
    network_mode: service:mesh
    ...
```

Docker does not restart containers that share another container's network after a reboot if they start before it. A small helper that starts them once the network container is up solves this (replace `<stack>` with the stack's Compose project name):

```yaml
  starter:
    image: docker:28-cli
    restart: unless-stopped
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    entrypoint:
      - /bin/sh
      - -c
      - |
        until [ -n "$$(docker ps -q --filter label=com.docker.compose.project=<stack> --filter label=com.docker.compose.service=mesh --filter status=running)" ]; do sleep 5; done
        sleep 10
        docker ps -aq --filter label=com.docker.compose.project=<stack> --filter label=com.docker.compose.service=backup --filter status=exited --filter status=created | xargs -r docker start
        exec sleep infinity
```

The helper mounts the Docker socket, which is root access to the host; see [Docker access](docker.md#docker-access).

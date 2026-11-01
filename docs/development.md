# Development

[Back to the README](../README.md)

```sh
go test ./...
TINNIE_DOCKER_TEST=1 go test ./internal/docker   # also runs the Docker client against the local daemon
test/e2e/run.sh                                   # builds the image and runs a full stack in Docker
```

The end-to-end suite ([test/e2e/compose.yml](../test/e2e/compose.yml)) backs up and restores Postgres 16, MariaDB 10.6, MySQL 8, a SeaweedFS bucket, a volume (owners, a symlink and an excluded folder) and a command source in one stack, with a service under `stop` and notifications to a webhook, and checks that each restore matches the snapshot exactly.

Images are published to GHCR by `.github/workflows/ci.yml`: `edge` from `main`, and `1`, `1.0`, `1.0.0` and `latest` from a `v1.0.0` tag (`latest` comes from docker/metadata-action's default flavor).

## Release checklist

- `go test ./...` and `test/e2e/run.sh` pass.
- The image tags in the docs still exist: `docker manifest inspect` each of `wordpress:7`, `mariadb:11` ([README](../README.md#quick-start), [config.md](config.md#type-volume)) and `docker:28-cli` ([network.md](network.md)). Move them to the current major version when a new one is out.
- The client versions in the docs match the `Dockerfile`: Postgres 16, 17 and 18, and the MariaDB 11.4 client.
- Tag `vX.Y.Z` and push the tag.
- `ghcr.io/mahdi-ninja/tinnie:1` pulls after the tag.

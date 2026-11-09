FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/tinnie . \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/tinnie-server ./cmd/tinnie-server

FROM restic/rest-server:0.14.0 AS rest-server

# tinnie-server: the backup server the stacks send their snapshots to. Built with --target server.
FROM alpine:3.23 AS server
RUN apk add --no-cache ca-certificates tzdata tini restic
COPY --from=rest-server /usr/bin/rest-server /usr/local/bin/rest-server
COPY --from=build /out/tinnie-server /usr/local/bin/tinnie-server
COPY docker/console-server.sh /etc/tinnie/console.sh
COPY --chmod=755 docker/bash /bin/bash
ENV ENV=/etc/tinnie/console.sh
VOLUME /data
EXPOSE 8000
HEALTHCHECK --interval=1m --timeout=30s --start-period=1m CMD ["tinnie-server", "health"]
ENTRYPOINT ["/sbin/tini", "--", "tinnie-server"]
CMD ["run"]

# tinnie: the backup container that runs inside each stack. The default target.
FROM alpine:3.23 AS tinnie
RUN apk add --no-cache ca-certificates tzdata tini postgresql16-client postgresql17-client postgresql18-client mariadb-client mariadb-connector-c openssh-client rclone restic
COPY --from=build /out/tinnie /usr/local/bin/tinnie
COPY docker/console.sh /etc/tinnie/console.sh
COPY --chmod=755 docker/bash /bin/bash
ENV ENV=/etc/tinnie/console.sh
HEALTHCHECK --interval=5m --timeout=30s --start-period=12h CMD ["tinnie", "health"]
ENTRYPOINT ["/sbin/tini", "--", "tinnie"]
CMD ["run"]

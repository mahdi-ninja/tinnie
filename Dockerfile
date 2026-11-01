FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/tinnie .

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata tini postgresql16-client postgresql17-client postgresql18-client mariadb-client mariadb-connector-c openssh-client rclone restic
COPY --from=build /out/tinnie /usr/local/bin/tinnie
COPY docker/console.sh /etc/tinnie/console.sh
COPY --chmod=755 docker/bash /bin/bash
ENV ENV=/etc/tinnie/console.sh
HEALTHCHECK --interval=5m --timeout=30s --start-period=12h CMD ["tinnie", "health"]
ENTRYPOINT ["/sbin/tini", "--", "tinnie"]
CMD ["run"]

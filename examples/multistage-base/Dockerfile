# A fuller build context for `zbox base build`: a multi-stage Go build with most Dockerfile directives in play.
# Build it with `zbox base build examples/multistage-base --name multi`, boot with `zbox new --base multi`, open port 8080.
# syntax=docker/dockerfile:1

# A global ARG with a default: each stage that wants it redeclares it without a value and inherits the default.
ARG GO_IMAGE=golang:1.25-alpine
ARG RUNTIME_IMAGE=alpine:3.20
ARG APP_VERSION=0.1.0

# ---- build stage: compiles the server, nothing of it reaches the base but the binary.
FROM ${GO_IMAGE} AS build
ARG APP_VERSION
WORKDIR /src
COPY src/go.mod ./
COPY src/*.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${APP_VERSION}" -o /out/server .

# ---- runtime stage: what the sandbox boots.
FROM ${RUNTIME_IMAGE}
ARG APP_VERSION
LABEL org.opencontainers.image.title="zbox multistage example" \
      org.opencontainers.image.version="${APP_VERSION}" \
      dev.zerops.zbox.example="multistage"

RUN apk add --no-cache ca-certificates curl tzdata \
 && addgroup -S app && adduser -S -G app app \
 && mkdir -p /srv/assets /etc/app /var/lib/app

# ADD of a local tar.gz is extracted in place, a URL would be downloaded.
ADD assets.tar.gz /srv/assets/
# A directory copy keeps its tree, a symlink inside the context stays a symlink (current.json -> app.json).
COPY config/ /etc/app/
# --chmod sets the mode no matter what the context had.
COPY --chmod=0755 scripts/entrypoint.sh /usr/local/bin/entrypoint
COPY --from=build /out/server /usr/local/bin/server

ENV APP_VERSION=${APP_VERSION} \
    APP_CONFIG=/etc/app/current.json \
    APP_ASSETS=/srv/assets \
    TZ=Europe/Prague

VOLUME /var/lib/app
WORKDIR /var/lib/app
EXPOSE 8080 9090
HEALTHCHECK --interval=10s --timeout=2s CMD curl -fs http://localhost:8080/healthz || exit 1

# USER is recorded in the image but the sandbox runs the workload as root: the agent does not switch users (see README).
USER app

ENTRYPOINT ["entrypoint"]
CMD ["server", "--listen", ":8080"]

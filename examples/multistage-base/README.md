# multistage-base

A fuller build context for `zbox base build`. The Dockerfile has two stages (a Go build and an Alpine runtime) and uses ARG before and after
FROM, LABEL, RUN, ADD of a local tar.gz (extracted), COPY of a directory with a symlink inside (kept as a symlink), COPY --chmod, COPY --from, ENV, VOLUME, WORKDIR,
EXPOSE with two ports, HEALTHCHECK, USER, ENTRYPOINT and CMD. The `.dockerignore` keeps `notes/`, `*.log` and this file out of the context the
build sees, `/tree` on the server shows what landed in the image.

```sh
zbox base build examples/multistage-base --name multi    # the first build pulls golang:1.25-alpine, a minute or two; rebuilds hit the cache
zbox base get multi                                      # labels, env, entrypoint and cmd, the two exposed ports
zbox new --base multi
zbox port open <id> 8080                                 # /, /healthz, /config, /assets/css/site.css, /tree
```

What the base keeps from the image: entrypoint, cmd, env, working dir, exposed ports and labels (`zbox base get`). What it does not use:
`USER` (the workload runs as root, the agent does not switch users), `HEALTHCHECK` (nothing probes it), `VOLUME` (the whole root is the
sandbox's writable disk anyway).

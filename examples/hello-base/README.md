# hello-base

The smallest useful build context for `zbox base build`: an Alpine image with Python and a one-file HTTP server on port 8080.

```sh
zbox base build examples/hello-base --name hello   # packs this directory, builds it on the workspace, registers the base
zbox new --base hello                              # boots a sandbox from it, the server starts as the workload
zbox port open <id> 8080                           # the port answers "hello from a built base: /"
```

A rebuild under the same name takes the name over, the earlier build stays until `zbox base rm`.

# zbox CLI

Agent sandboxes on Zerops. The CLI creates a sandbox pool in your Zerops account and manages sandboxes inside it. One Zerops service is one sandbox pool.

```sh
go build -o zbox .

zbox login                 # browser login, or: zbox login --token <PAT>
zbox setup                 # creates the Zerops project with the sandbox pool
zbox setup --project-id <id>  # adds a pool to an existing project instead (--name sets the service hostname)
zbox new mybox             # sandbox in the pool, from the default agent image (Ubuntu + Claude Code); offers to create a pool when there is none
zbox ls
zbox exec <id> -- claude --version
zbox shell <id>
zbox rm <id>
zbox teardown              # deletes the Zerops project
```

A pool whose project was deleted elsewhere is unassigned automatically by `zbox status` and `zbox teardown`.

State lives in `~/.config/zbox/config.json` (override with `ZBOX_CONFIG`). `ZBOX_TOKEN` overrides the stored token.

Hidden flags: `--public-api-url` (default futon), `--gui-url`, `--binary-url`.

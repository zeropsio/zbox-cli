# zbox CLI

Agent sandboxes on Zerops. The CLI creates a sandbox workspace in your Zerops account and manages the sandboxes inside it. One Zerops service is one workspace.

```sh
go build -o zbox .

zbox login                    # browser login, or: zbox login --token <PAT>
zbox setup                    # creates the Zerops project with the workspace and its default base
zbox setup --project-id <id>  # adds a workspace to an existing project instead (--name sets the service hostname)
zbox workspace use <url> --api-key <key>   # or point at a workspace someone else created

zbox base add ubuntu:24.04    # any OCI image becomes a base
zbox new mybox -p 3000        # sandbox from the only base, port 3000 guarded by the workspace API key
zbox new mybox -p 3000/public --base ubuntu --network project_internet --idle-ttl 30m
zbox ls
zbox get <id>                 # one URL per declared port: https://3000-<id>-<ingress-host>
zbox port open <id> 8080/ticket
zbox exec <id> -- claude --version
zbox shell <id>
zbox park <id>                # disk and memory kept, zero RAM; a request wakes it
zbox fork <id> --count 3
zbox snapshot create <id> before-refactor && zbox snapshot restore <snapshot-id>
zbox events --follow
zbox rm <id>
zbox teardown                 # deletes the Zerops project
```

Every command takes `--json` and prints the API objects as they are. Mutations wait for their operation unless `--no-wait`.

Also there: `template`, `policy`, `credential` (tickets and tokens for preview ports), `integration` and `attach` (credential proxy), `webhook`, `session`, `process`, `file`, `ssh`, `lineage`, `export`, `import`, `metrics`, `quarantine`.

State lives in `~/.config/zbox/config.json` (override with `ZBOX_CONFIG`). `ZBOX_TOKEN` overrides the stored Zerops token, `ZBOX_WORKSPACE_URL` and `ZBOX_API_KEY` the workspace.

The API client is generated from the orchestrator's OpenAPI file, see `CLAUDE.md`.

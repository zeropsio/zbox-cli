# zbox CLI

Agent sandboxes on Zerops. The CLI creates a sandbox workspace in your Zerops account and manages the sandboxes inside it. One Zerops service is one workspace.

```sh
go build -o zbox .

zbox login                    # through the Zerops GUI, or: zbox login --token <PAT>, or --email
zbox setup                    # creates the Zerops project with the workspace and its default base
zbox setup --project-id <id>  # adds a workspace to an existing project instead (--name sets the service hostname)
zbox setup --template dev:4g:2  # also builds a warm template named dev (4 GB, 2 cpus) before the workspace starts
zbox workspace use <url> --api-key <key>   # or point at a workspace someone else created (--name names it)
zbox workspace ls             # the workspaces of the account, the ones with a stored key are named
zbox workspace authorize <service-id>      # fetch the key of one through the Zerops GUI and store it
zbox switch <name>            # make a stored workspace the current one
zbox open                     # the console the workspace serves, signed in with the workspace key

zbox base add ubuntu:24.04    # any OCI image becomes a base
zbox new mybox -p 3000        # sandbox from the only base, port 3000 guarded by the workspace API key
zbox new mybox -p 3000/public --base ubuntu --network project_internet --idle-ttl 30m
zbox ls
zbox get <id>                 # one URL per declared port: https://3000-<id>-<ingress-host>
zbox port open <id> 8080/ticket
zbox connector login claude   # browser login in a helper sandbox, the credential stays in the workspace
zbox new --integration claude # the sandbox talks to a proxied endpoint, never sees the credential
zbox exec <id> -- claude --version
zbox shell <id>
zbox park <id>                # disk and memory kept, zero RAM; a request wakes it
zbox fork <id> --count 3
zbox snapshot create <id> before-refactor && zbox snapshot restore <snapshot-id>
zbox events --follow
zbox rm <id>
zbox teardown                 # deletes the workspace service, the Zerops project stays
```

Every command takes `--json` and prints the API objects as they are. Mutations wait for their operation unless `--no-wait`.

Also there: `template`, `policy`, `credential` (tickets and tokens for preview ports), `connector` (claude, github, codex logins), `integration` and `attach` (credential proxy), `webhook`, `session`, `process`, `file`, `ssh`, `lineage`, `export`, `import`, `metrics`, `quarantine`.

State lives in `~/.config/zbox/config.json` (override with `ZBOX_CONFIG`). `ZBOX_TOKEN` overrides the stored Zerops token, `ZBOX_WORKSPACE_URL` and `ZBOX_API_KEY` the workspace.

The API client is generated from the orchestrator's OpenAPI file, see `CLAUDE.md`.

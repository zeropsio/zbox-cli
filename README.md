# zbox CLI

Agent sandboxes on Zerops. The CLI creates a sandbox workspace in your Zerops account and manages the sandboxes inside it. One Zerops service is one workspace.

```sh
go install ./cmd/zbox         # or from anywhere: go install github.com/zeropsio/zbox-cli/cmd/zbox@latest (the repo is private: GOPRIVATE=github.com/zeropsio)

zbox login                    # through the Zerops GUI, or: zbox login --token <PAT>, or --email
zbox setup                    # creates the Zerops project with the workspace and its default base
zbox setup --project-id <id>  # adds a workspace to an existing project instead (--name sets the service hostname)
zbox setup --template dev:4g:2  # builds only a warm template named dev (4 GB, 2 cpus); the default builds dev:4g:2 and big:16g:4
zbox setup --cpu 2 --ram 8 --disk 50  # a small workspace VM instead of the default 24 cpu, 128 GB, 250 GB
zbox setup --warmup dev='claude --version'  # the dev template runs this before its memory is captured, so claude is resident in every spawn
zbox workspace use <url> --api-key <key>   # or point at a workspace someone else created (--name names it)
zbox workspace ls             # the workspaces of the account, the ones with a stored key are named
zbox workspace authorize <service-id>      # fetch the key of one through the Zerops GUI and store it
zbox switch <name>            # make a stored workspace the current one
zbox open                     # the console the workspace serves, signed in with the workspace key

zbox base add ubuntu:24.04    # any OCI image becomes a base
zbox new mybox -p 3000        # sandbox from the only base, port 3000 guarded by the workspace API key
zbox new mybox -p 3000/public --base ubuntu --network project_internet --idle-ttl 30m
zbox template create --name node --base ubuntu --warm --warmup "node -e \"require('http')\""  # warm template, the runtime loaded before the snapshot
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

claude mcp add zbox -- zbox mcp --template dev   # MCP server on stdio: run_code (fresh VM per call, or kept) and kill_sandbox
```

Every sandbox argument takes the id or the name (`zbox shell app`). Every command takes `--json` and prints the API objects as they are. Mutations wait for their operation unless `--no-wait`.

Also there: `template`, `policy`, `credential` (tickets and tokens for preview ports), `connector` (claude, github, codex logins), `integration` and `attach` (credential proxy), `webhook`, `session`, `process`, `file`, `ssh`, `lineage`, `export`, `import`, `metrics`, `quarantine`.

State lives in `~/.config/zbox/config.json` (override with `ZBOX_CONFIG`). `ZBOX_TOKEN` overrides the stored Zerops token, `ZBOX_WORKSPACE_URL` and `ZBOX_API_KEY` the workspace.

The API client is generated from the orchestrator's OpenAPI file, see `CLAUDE.md`.

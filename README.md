# zbox CLI

Agent sandboxes on Zerops. The CLI creates a sandbox workspace in your Zerops account and manages the sandboxes inside it. One Zerops service is one workspace, a VM that runs every sandbox as a Firecracker guest.

```sh
go install ./cmd/zbox         # or from anywhere: go install github.com/zeropsio/zbox-cli/cmd/zbox@latest (the repo is private: GOPRIVATE=github.com/zeropsio)

zbox login                    # through the Zerops GUI, or: zbox login --token <PAT>, or --email; zbox register creates an account
zbox setup                    # creates the Zerops project with the workspace, its default base and the dev and big warm templates
zbox setup --project-id <id>  # adds a workspace to an existing project instead (--name sets the service hostname)
zbox setup --template dev:4g:2  # builds only a warm template named dev (4 GB, 2 cpus); the default builds dev:4g:2 and big:16g:4
zbox setup --cpu 2 --ram 8 --disk 50  # a small workspace VM instead of the default 24 cpu, 128 GB, 250 GB
zbox setup --warmup dev='claude --version'  # the dev template runs this before its memory is captured, so claude is resident in every spawn
zbox status                   # the login, the workspace, whether it answers, its capacity and what is in use

zbox workspace ls             # the workspaces of the account, the ones with a stored key are named
zbox workspace authorize <service-id>      # fetch the key of one through the Zerops GUI and store it
zbox workspace use <url> --api-key <key>   # or point at a workspace someone else created (--name names it)
zbox switch <name>            # make a stored workspace the current one
zbox ls --workspace <name>    # or work on another stored workspace for one run (env ZBOX_WORKSPACE), the current one stays
zbox open                     # the console the workspace serves, signed in through a one-time ticket
zbox workspace key            # the workspace API key, for the console or another machine

zbox base add ubuntu:24.04    # any OCI image becomes a base
zbox new mybox -p 3000        # sandbox from the only base, port 3000 guarded by the workspace API key
zbox new app --template dev -p 3000/public --memory 2g --idle-ttl 30m  # resumes the template's memory, a public port
zbox template create --name node --base ubuntu:24.04 --warm --warmup "node -e \"require('http')\""  # warm template, the runtime loaded before the snapshot
zbox ls
zbox get app                  # every sandbox argument is the id or the name; one URL per declared port: https://3000-<id>-<ingress-host>
zbox port open app 8080/ticket && zbox cred issue app --port 8080  # a port behind a one-use preview ticket, the URL printed with it
zbox connector login claude   # browser login in a helper sandbox, the credential stays in the workspace
zbox new --integration claude # the sandbox talks to a proxied endpoint, never sees the credential
zbox exec app -- claude --version
zbox shell app
zbox park app                 # disk and memory kept, zero RAM; a request wakes it, or zbox wake
zbox fork app --count 3
zbox snapshot create app before-refactor --with-memory && zbox snapshot restore <snapshot-id> --with-memory
zbox operation ls             # what the mutations did and how long they took (--limit, --all)
zbox events --follow
zbox rm app
zbox teardown                 # deletes the workspace service, the Zerops project stays

claude mcp add zbox -- zbox mcp --template dev   # MCP server on stdio: run_code (fresh VM per call, or kept) and kill_sandbox
```

Every command takes `--json` and prints the API objects as they are. Mutations wait for their operation and exit non-zero when it fails, unless `--no-wait`.

Also there: `set` (rename, TTLs), `policy`, `template`, `credential` (tickets and tokens for preview ports), `connector` (claude, github, codex logins), `integration`, `attach`, `detach` and `attachments` (credential proxy), `webhook`, `session`, `process`, `file`, `ssh`, `ssh-key`, `lineage`, `export`, `import`, `metrics`, `quarantine`.

State lives in `~/.config/zbox/config.json` (override with `ZBOX_CONFIG`). `ZBOX_TOKEN` overrides the stored Zerops token, `ZBOX_WORKSPACE` (or `--workspace`) picks a stored workspace for one run, `ZBOX_WORKSPACE_URL` and `ZBOX_API_KEY` override the workspace entirely.

The API client is generated from the orchestrator's OpenAPI file, see `CLAUDE.md`.

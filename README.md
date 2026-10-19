# zbox CLI

**Beta.** zbox and Zerops Box are in beta: behaviour, commands, flags and the API can change without notice.

Agent sandboxes on Zerops. The CLI creates a Zerops Box workspace in your Zerops account and manages the sandboxes inside it. One Zerops service is one workspace, a VM that runs every sandbox as a Firecracker guest.

## Install

One line on Linux and macOS (amd64 and arm64), it downloads the latest release, checks its sha256 and puts `zbox` into `/usr/local/bin` when that is writable, else `~/.local/bin`:

```sh
curl -fsSL https://box.zerops.io/install | sh
```

On Windows (amd64 and arm64) in PowerShell, into `%LOCALAPPDATA%\Programs\zbox` and onto your user `PATH`:

```powershell
irm https://box.zerops.io/install.ps1 | iex
```

`ZBOX_VERSION=v0.24` pins a release and `ZBOX_INSTALL_DIR` picks the directory, for both scripts. A tag with a hyphen (`v0.24-rc1`) is published as a pre-release: the scripts skip it unless `ZBOX_VERSION` names it. The scripts are [install.sh](install.sh) and [install.ps1](install.ps1) in this repo, the binaries and `checksums.txt` are on the [releases page](https://github.com/zeropsio/zbox-cli/releases), one per target (`zbox-linux-amd64`, `zbox-darwin-arm64`, `zbox-windows-amd64.exe`, ...), so a download by hand works too. `zbox --version` says what is installed.

From source, with Go 1.25 or newer (`make install` builds into `~/.local/bin`, `go install ./cmd/zbox` into `$(go env GOPATH)/bin`):

```sh
git clone https://github.com/zeropsio/zbox-cli.git
cd zbox-cli
make install
```

Or straight with `go install`:

```sh
go install github.com/zeropsio/zbox-cli/cmd/zbox@latest
```

## Usage

```sh
zbox login                    # confirm a code in the Zerops GUI, or: zbox login --token <PAT>, or --email; zbox register creates an account
zbox setup                    # creates a Zerops project (zbox-<timestamp>, --project names it) with the workspace and pulls the default base
zbox setup --project-id <id>  # adds a workspace to an existing project instead (--name sets the service hostname, zbox by default)
zbox setup --base hello=ubuntu:26.04 --template dev=2:4g@hello  # pulls a base named hello and builds a warm template dev on it (2 cpus, 4 GiB); none are built by default
zbox setup --cpu 4 --ram 8 --disk 50  # pins the size of the workspace VM, the platform's defaults for the type otherwise (2 cpus, 4 GiB, 10 GiB)
zbox setup --warmup dev='claude --version'  # the dev template runs this before its memory is captured, so claude is resident in every spawn
zbox setup --build app=./app   # builds the Dockerfile context into a base named app on the new workspace (.dockerignore honoured); zbox base build does it later
zbox workspace status         # the login, the workspace, whether it answers, its capacity and what is in use

zbox workspace list           # the workspaces of the account, the stored ones are named
zbox workspace authorize <service-id>      # store one of the account here: it is reached through zproxy with the login, no key involved
zbox workspace use <url> --api-key <key>   # or point at a workspace outside the account by URL and key (--name names it)
zbox workspace switch <name>  # make a stored workspace the current one
zbox list --workspace <name>  # or work on another stored workspace for one run (env ZBOX_WORKSPACE), the current one stays
zbox workspace console        # the console the workspace serves, signed in through zproxy
zbox workspace key            # the workspace API key, read from the service for `use --api-key` on a machine without the login
zbox setup --no-subdomain     # a workspace without subdomain access: reached through zproxy all the same, but its sandbox ports get no public URL
zbox workspace domain list    # where sandbox ports are published: the ingress subdomain (dash form, added by setup) and own wildcard domains
zbox workspace domain add sb.example.com   # *.sb.example.com routed to port 8081 in Zerops gives every port https://<port>-<id>.sb.example.com

zbox base add ubuntu:24.04    # any OCI image becomes a base
zbox new mybox -p 3000        # sandbox from the only ready base (--base picks one), port 3000 opens only with a one-time ticket (zbox open mybox)
zbox new app --template dev -p 3000/public --memory 2g --idle-ttl 30m  # resumes the template's memory, a public port
zbox template create --name node --base ubuntu:24.04 --warm --warmup "node -e \"require('http')\""  # warm template, the runtime loaded before the snapshot
zbox list                     # or zbox ls: every list, remove and rename command has its short form (ls, rm, mv)
zbox get app                  # every sandbox argument is the id or the name; one URL per declared port and domain: https://3000-<id>-<ingress-host>
zbox open app 3000            # the port in the browser, entered with a 60 s one-time ticket minted for this opening
zbox port open app 8080/ticket && zbox cred issue app --port 8080  # a port behind a one-use preview ticket, the URL printed with it
zbox connector login claude   # browser login in a helper sandbox, the credential stays in the workspace
zbox new --integration claude # the sandbox talks to a proxied endpoint, never sees the credential
zbox exec app -- claude --version
zbox shell app
zbox park app                 # disk and memory kept, zero RAM; a request wakes it, or zbox wake
zbox fork app --count 3
zbox snapshot create app before-refactor --with-memory && zbox snapshot restore <snapshot-id> --with-memory
zbox operation list           # what the mutations did and how long they took (--limit, --all)
zbox events --follow
zbox remove app               # or zbox rm
zbox workspace teardown       # deletes the workspace service, the Zerops project stays

claude mcp add zbox -- zbox mcp --template dev   # MCP server on stdio: run_code (fresh VM per call, or kept) and kill_sandbox
```

Output is a table on a terminal and tab-separated with a header when piped; `--json` prints the API objects as they are, `-q` only the ids (`id=$(zbox new -q --template dev)`). Mutations wait for their operation (`--timeout` bounds the wait, `--no-wait` skips it) and exit non-zero when it fails. Destructive commands ask on a terminal and need `--yes` otherwise. Commands are spelled out (`list`, `remove`, `rename`, `workspace`, `operation`, `credential`, `snapshot`, `integration`) and take their short forms as aliases (`ls`, `rm`, `mv`, `ws`, `op`, `cred`, `snap`, `int`); everything that manages a workspace sits under `zbox workspace`, `zbox setup` is the one convenience at the top. Exit codes and the rest of the output contract: `zbox --help` and [docs/cli-ux.md](docs/cli-ux.md).

Also there: `rename` (or `mv`), `set` (rename, TTLs), `policy`, `template`, `credential` (tickets and tokens for preview ports), `connector` (claude, github, codex logins), `integration`, `attach`, `detach` and `attachments` (credential proxy), `webhook`, `session`, `process`, `ps` (the guest's processes), `file`, `ssh`, `ssh-config`, `lineage`, `metrics`, `quarantine`.

### SSH

`zbox ssh <id|name>` opens SSH to a sandbox through the API: the local `ssh` runs with a ProxyCommand that carries the connection over a WebSocket to the SSH server inside the sandbox's agent, so it works in every network mode, without the project VPN and without a key. The host key is pinned from the API. Arguments after `--` go to `ssh`, a trailing command runs instead of the shell:

```sh
zbox ssh app
zbox ssh app -- uname -a
zbox ssh app -- -L 3000:localhost:3000
```

`zbox ssh-config --install` writes a `Host zbox-*` block next to the zbox config and includes it from `~/.ssh/config`, after which every tool that speaks SSH reaches a sandbox of the current workspace as `zbox-<id>`: `ssh zbox-<id>`, `scp`, `sftp`, `sshfs`, `rsync` (needs rsync in the image), VS Code Remote-SSH and JetBrains Gateway. Sessions run as root with the sandbox's environment. A parked sandbox is woken by the connection. `zbox ssh-key` is for an image that runs its own sshd.

### Parking and secrets

`zbox park app` keeps a memory image, the wake resumes the sandbox where it was in under a second. `zbox park app --no-memory` shuts the guest down instead: the disk stays, everything running inside ends, and the next wake is a cold boot that picks up the current secrets, workspace env and policy. A sandbox whose secret selection changed since its last delivery is stale: anything that would wake it implicitly (ssh, the shell, a request) answers `secretsStale`, and only `zbox wake app --redeliver-secrets` boots it with the new selection.

The workspace is a managed Zerops service (`zbox@1`, type "Box Workspace"): nothing is deployed to it, the orchestrator ships with the platform and starts on its own. Bases are registered through the API (`zbox base add` for an image reference or a `docker save` tarball URL, `zbox base build` for a Dockerfile context), `zbox setup` pulls the default image unless `--base` or `--build` name others; templates likewise (`zbox setup --template` once the bases are in, `zbox template create --warm` any time). A service reload keeps the sandboxes up, a restart reboots the VM and parks every sandbox.

State lives in `~/.config/zbox/config.json` (override with `ZBOX_CONFIG`). `ZBOX_TOKEN` overrides the stored Zerops token, `ZBOX_WORKSPACE` (or `--workspace`) picks a stored workspace for one run, `ZBOX_WORKSPACE_URL` and `ZBOX_API_KEY` override the workspace entirely.

The API client is generated from the orchestrator's OpenAPI file, see `CLAUDE.md`.

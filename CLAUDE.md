# zbox CLI

The CLI is a wrapper around the Zerops Box orchestrator: one Zerops service of the type "Box Workspace" (`zbox@1`) is one workspace, a VM that runs every sandbox as a Firecracker guest. Workspaces are created and deleted through the Zerops public API, the sandboxes inside one through the orchestrator's REST API, reached through zproxy with a ui-access bearer (never the workspace key, never a token in a URL). Read [docs/cli-ux.md](docs/cli-ux.md) for the output and interaction rules every command follows. The rules below are the ones that bite when they are missed.

- The CLI is built against the contract in `internal/api/openapi.yml`. Nothing exists in the CLI that is not in the spec, and a shape that is wrong for the CLI is fixed on the orchestrator side, never worked around here. After a spec change run `go generate ./internal/api`; never edit `internal/api/gen.go` by hand.
- Follow `docs/cli-ux.md` when adding or changing a command, and change that file in the same commit when a command does not fit it.
- Say "workspace", never "pool". A sandbox is terminated, everything else is deleted. Durations are printed at millisecond resolution, never in whole seconds.
- Build into the user's PATH after every change, they test by hand from there:

```sh
go build -o ~/.local/bin/zbox ./cmd/zbox
```

- Run `go vet ./...` and `go test ./...` before reporting a change as done.

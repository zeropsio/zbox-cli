# zbox CLI

Wrapper around the zbox orchestrator, sibling of `../zbox-gui`. The orchestrator itself lives in `../nest` (`pkg/zerops/orchestrator/zbox`), design notes and decisions in `~/.claude/plans/zbox.md`.

Terminology: one Zerops service is one **sandbox pool**. Say "pool" in code and output. The persisted keys (`serviceId` in `config.json`) keep the old `service` spelling so existing assignments survive the rename.

## Iteration loop

Build straight into the user's PATH after every change, they test by hand from there:

```sh
go build -o ~/.local/bin/zbox .
```

The GUI is usually running locally at the same time (`npm run dev` in `../zbox-gui`, port 5173). The browser login hand-off goes to `guiUrl` from `~/.config/zbox/config.json`, which is set to `http://localhost:5173` on this machine so the installed CLI hands off to the dev server. Drop the key to fall back to the deployed subdomain, or override one run with `--gui-url`.

Hidden flags for the rest: `--public-api-url` (default futon), `--binary-url` (orchestrator download URL baked into the import YAML).

## Deploy targets on futon

Everything lives in the futon project `zbox-plumbing` (`44MdeqXlSqq1sLbN6G5sxQ`, org `fXFTnhQeTQK5USIxoUnP8Q`). `zcli` is logged in to that account; a futon PAT is handed over in-session and must never be written into either repo.

Bucket `57fhv-s3` of the `s3` service (`V4GTKbk4TwOA43M9ZMWfYg`) is anonymous-readable and serves two objects over `https://grg.zerops.dev/57fhv-s3/`. Credentials come from the s3 service envs, pass them as `MC_HOST_<alias>=https://<key>:<secret>@grg.zerops.dev` so they never land in `~/.mc/config.json`.

`zbox` is the orchestrator binary the import YAML downloads on every service start. Re-upload only when the orchestrator changed, never for CLI-only or GUI-only work:

```sh
cd ../nest && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /tmp/zbox ./cmd/zerops/orchestrator/zbox
mc cp /tmp/zbox <alias>/57fhv-s3/zbox
```

`zbox-base.tar.gz` is a `docker save` of the default sandbox image, which the orchestrator loads instead of building it from the embedded Dockerfile. Rebuild it whenever `assets/Dockerfile` in the orchestrator changes. `--provenance=false --sbom=false` keeps it a plain single-platform image, docker 26.1 in the pool cannot load a buildx manifest list:

```sh
cd ../nest/pkg/zerops/orchestrator/zbox/service/sandboxManager/assets
docker build --platform linux/amd64 --provenance=false --sbom=false -t zbox-base:latest .
docker save zbox-base:latest | gzip -1 > /tmp/zbox-base.tar.gz
mc cp /tmp/zbox-base.tar.gz <alias>/57fhv-s3/zbox-base.tar.gz
```

## Default sandbox image

A pool is only usable once `zbox-base:latest` is in its docker. The import YAML runs `/var/www/zbox prepare-image` as an init command, so the service reaching ACTIVE already has the image, and `zbox setup` waits for `ping.imageReady` before it calls the pool ready. The orchestrator never fetches the image on its own, `zbox run` only reports what it finds.

Pools imported before this (their stored initCommands have no prepare-image step) stay stuck on `imageNotReady` forever, a restart does not heal them. Recreate them with `zbox teardown` plus `zbox setup`.

The same YAML is duplicated in `../zbox-gui` (`src/lib/pool.svelte.ts`, `importYaml`), keep the two in step.

## Sandbox ports

A sandbox exposes 0-n ports and starts with none. `zbox new -p 8080 -p 3000` opens them up front, `zbox port open|close|ls <id>` changes them at any point after that. Nothing is published on the host: the set is an allow list the pool's proxy checks, so opening a port neither restarts nor touches the container.

The API replaces the whole set (`PUT /api/rest/sandbox/{id}/ports`), never one port at a time, so two callers editing the same sandbox cannot merge into a list neither asked for. The CLI subcommands read the set and write it back changed.

Each port has its own URL, `{pool}/s/{port}-{id}/`, which mirrors the `{port}-{id}-{ingress-host}` subdomain the design lands on later. There is no sandbox-level URL any more, `zbox get` prints one line per open port.

## The setup wait

`zbox setup` takes minutes and has almost nothing to print, so `internal/waiting` draws it: a wavy `zbox` wordmark, a progress estimate and a rotating note. Rules that package lives by:

- The frame is redrawn in place by counting lines, so every frame must have the same height and no line may exceed the terminal width. `screen_test.go` guards both. While the frame is up, nothing may reach stdout except through `Step` and `Detail`, a stray `fmt.Println` lands in the middle of it.
- Stdout that is not a TTY (a pipe, CI) falls back to plain lines, and `NO_COLOR` drops the colour.
- The percentage is a curve against `--estimate` (3m) that reaches 95% there and creeps afterwards, never 100%. The stage floors in `cmd/setup.go` pull it forward when a stage lands early.
- The Zerops processes behind the import are deliberately not shown, the step name is the whole status. A failed process still fails the setup with its own message.
- The notes are duplicated in `../zbox-gui` (`src/lib/notes.ts`), keep them in step. They are only ever accurate, useful things about a pool: no jokes, no filler, and never soften a cost or a restart. Two wrapped lines each is the room they get.

## Gotcha

The public API answers a deleted project or service with **HTTP 400 and code `projectNotFound` / `serviceStackNotFound`**, not 404, even though the ACL middleware that raises them was meant to answer 404. `zerops.IsNotFound` matches the code and accepts 404 too, so a fix on the API side keeps working. Use it instead of checking the status.

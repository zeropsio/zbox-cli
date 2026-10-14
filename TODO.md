# TODO

## zproxy access (started 2026-10-13)

- [ ] Test the whole path on a live workspace (the four stored ones were torn down, see CLAUDE.md; `zboxapi` in `zbox-rel` is the candidate): setup, `--no-subdomain`, shell and events through the tunnel, `base build` upload, an access left to expire for over an hour and renewed on the next command, `zbox workspace console`.
- [x] The frontend removed the `workspace` intent of `/authorize-app` and its codes on 2026-10-13, and the whole zbox mode of the page later that day once the CLI moved to the device login (uncommitted on its `z0-zbox`).
- [ ] Device login follow-ups: a `zbox login` on a machine without a browser prints the URL and the code only, which is fine for ssh, but the poll gives no feedback for 5 minutes; a 2FA account cannot use `--email`, which the help says.
- [ ] Replace the hand-made `zerops.Client.UiAccess` with the SDK call once a `zerops-go` release carries `PostServiceStackUiAccess` and `urlApi`.
- [ ] `zbox workspace status --workspace <name>` drops the entry when the service answers not-found (`forgetGoneWorkspace`), which it also does for a workspace of another account; a listing-only command should probably not forget anything.
- [x] `zbox workspace authorize` reads the API host from the service env `zeropsSubdomain` since 2026-10-14, no orchestrator call beyond the access mint.
- [x] Own wildcard domain end to end: `*.sb.matejpavlicek.cz` to 8081 and `sb.matejpavlicek.cz` to 8080 on futon, public and members ports with tickets on both domains, subdomain access off and on, verified 2026-10-14.

## Multi-target deletes (decided 2026-10-09)

Delete commands keep taking several ids, as `rm`, `docker rm` and `kubectl delete` do; safety comes from the rails below, not from one id per call. All of them (`remove`, `base remove`, `template remove`, `snapshot remove`, `file remove`, `webhook remove`, `integration remove`, `quarantine remove`, `cred revoke`, `detach`) go through `deleteEach` in `cmd/errors.go` since the review of 2026-10-09, so each item below is a change in that one function (and the rule in `docs/cli-ux.md` §5).

- [ ] Resolve every reference before the first deletion, so a typo in the last one fails before anything is touched (sandboxes already are, through `sandboxArgsAll`; base and template remove resolve first too; snapshot, webhook, integration, quarantine, credential and file ids go to the API one by one).
- [ ] The confirmation names every target, id and name: `Terminate 3 sandboxes (app, worker, x7k2abcd)?` (bases and templates already print `digest (name)` through `named`).
- [ ] Continue past an item that fails, print one result line per item (`x terminated in 70 ms` / `y: <error>`), exit non-zero with the code of the first failure.
- [ ] Under `--json` add the failed items as `{id, error}` to the `[{id, operation}]` array `deleteEach` prints, so the sandboxes already terminated before a failure are reported too.

- [ ] SSH (2026-10-14): `~/.config/zbox/known_hosts` keeps a `zbox-<id>` line for every sandbox ever pinned, prune the lines of terminated sandboxes on
      `zbox rm` or on the next pin; `zbox ssh-config` writes `ProxyCommand` with the absolute path of the running binary, a moved or upgraded binary
      needs `--install` again, consider `zbox` from PATH when it resolves to the same file; `ForwardAgent` is not in the block, `zbox ssh app -- -A`
      works, document or add a `--forward-agent` flag; the `--workspace` of a `zbox ssh` run is baked into that ProxyCommand only, the installed
      block always uses the current workspace.

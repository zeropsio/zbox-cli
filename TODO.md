# TODO

Open items, in no particular order.

## Login and workspaces

- `zbox login` on a machine without a browser prints the URL and the code and then polls for up to five minutes without any feedback; print something while it waits.
- A two-factor account cannot use `zbox login --email`, which the help says; the device login is the way.
- Replace the hand-made `zerops.Client.UiAccess` with the SDK call once a `zerops-go` release carries `PostServiceStackUiAccess` and `urlApi`.
- `zbox workspace status --workspace <name>` drops the stored entry when the service answers not-found (`forgetGoneWorkspace`), which it also does for a workspace of another account; a read-only command should probably not forget anything.

## Multi-target deletes

Delete commands take several ids, as `rm`, `docker rm` and `kubectl delete` do; safety comes from the rails below, not from one id per call. All of them (`remove`, `base remove`, `template remove`, `snapshot remove`, `file remove`, `webhook remove`, `integration remove`, `quarantine remove`, `cred revoke`, `detach`) go through `deleteEach` in `cmd/errors.go`, so each item is a change in that one function and in §5 of `docs/cli-ux.md`.

- Resolve every reference before the first deletion, so a typo in the last one fails before anything is touched. Sandboxes, bases and templates already are; snapshot, webhook, integration, quarantine, credential and file ids still go to the API one by one.
- The confirmation names every target, id and name: `Terminate 3 sandboxes (app, worker, x7k2abcd)?`. Bases and templates already print `digest (name)`.
- Continue past an item that fails, print one result line per item (`x terminated in 70 ms` / `y: <error>`), and exit non-zero with the code of the first failure.
- Under `--json`, add the failed items as `{id, error}` to the `[{id, operation}]` array, so the targets deleted before a failure are reported too.

## SSH

- `~/.config/zbox/known_hosts` keeps a `zbox-<id>` line for every sandbox ever pinned; prune the lines of terminated sandboxes on `zbox rm` or on the next pin.
- `zbox ssh-config` writes the `ProxyCommand` with the absolute path of the running binary, so a moved or upgraded binary needs `--install` again; use `zbox` from `PATH` when it resolves to the same file.
- `ForwardAgent` is not in the installed block. `zbox ssh app -- -A` works; document it or add a `--forward-agent` flag.
- The `--workspace` of a `zbox ssh` run is baked into that run's `ProxyCommand` only; the installed `Host zbox-*` block always uses the current workspace.

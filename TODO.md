# TODO

## Multi-target deletes (decided 2026-10-09)

Delete commands keep taking several ids, as `rm`, `docker rm` and `kubectl delete` do; safety comes from the rails below, not from one id per call. All of them (`rm`, `base rm`, `template rm`, `snapshot rm`, `file rm`, `webhook rm`, `integration rm`, `quarantine rm`, `cred revoke`, `detach`) go through `deleteEach` in `cmd/errors.go` since the review of 2026-10-09, so each item below is a change in that one function (and the rule in `docs/cli-ux.md` §5).

- [ ] Resolve every reference before the first deletion, so a typo in the last one fails before anything is touched (sandboxes already are, through `sandboxArgsAll`; base and template rm resolve first too; snapshot, webhook, integration, quarantine, credential and file ids go to the API one by one).
- [ ] The confirmation names every target, id and name: `Terminate 3 sandboxes (app, worker, x7k2abcd)?` (bases and templates already print `digest (name)` through `named`).
- [ ] Continue past an item that fails, print one result line per item (`x terminated in 70 ms` / `y: <error>`), exit non-zero with the code of the first failure.
- [ ] Under `--json` add the failed items as `{id, error}` to the `[{id, operation}]` array `deleteEach` prints, so the sandboxes already terminated before a failure are reported too.

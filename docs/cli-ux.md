# zbox CLI: output and interaction rules

zbox is used by people at a terminal and by agents and scripts through pipes, and both get first-class treatment. Every command follows the rules below. When a new command does not fit them, change this file in the same commit, so the rules and the code never disagree.

The helpers named here live in `cmd/output.go` and `cmd/errors.go`. Use them, do not print with `fmt` directly.

## 1. Three output forms, chosen by the destination

| stdout is | form | how |
|---|---|---|
| a terminal | ASCII tables (`+---+`), relative and local times, short prose | `pretty()` is true |
| a pipe or a file | tab-separated values with one header line, absolute UTC times, no borders, no colour | `pretty()` is false |
| any, with `--json` | the API objects untouched, indented | `emit(v, text)` |

- **Tables, not CSV, when piped.** A TSV row is what `cut -f`, `awk -F'\t'` and `sort -t$'\t'` take without a parser. An agent reads it at about half the tokens of a bordered table, and it needs no quoting because cells never hold a tab or a newline (`plainRow` turns them into spaces). gh does the same when piped. Anything more structured is `--json`.
- **Empty cells are `-`**, never blank, so the column count of a row never shrinks.
- **Ids are never shortened.** Digests are the 12-character prefix, which every command accepts back as a reference.
- **Listings** use `newTable(cols...)`, `.row(cells...)` and `.print()`. The first column is the key (`.keyCol(i)` when it is not), and the column order is stable: identity first, `CREATED` last.
- **Detail views** (`get`, the result of a create) use `fields(label, value, ...)`. That is a two-column table on a terminal and `label<TAB>value` lines when piped. The first pair is the object's key. Nested structures (a policy, an operation result, a launch contract) come after it via `section(title, v)`: indented JSON on a terminal, a compact `title<TAB>{json}` line when piped.
- **Timestamps** go through `when()`. On a terminal they read `3m ago (2026-10-09 14:02:11)` (or `in 5m (...)`) in local time. Piped, they are RFC 3339 in UTC. Durations go through `took()`/`durationString()` at millisecond resolution, and a duration is never printed in whole seconds.
- **`--json`** prints exactly what the API answered, in the API's order. It never prints prose, progress or hints. A streamed list (`events --follow --json`) is NDJSON, one object per line.

## 2. stdout is the result, stderr is everything else

- `stdout`: the table, the detail view, the result line of a mutation (`result(id, "...")`), and nothing else.
- `stderr` through `note()`: progress (`Building, operation x.`), hints (`No sandboxes. zbox new creates one.`), what to do next, and warnings. `note` is silent under `--json` and `--quiet`, because a caller may read stderr together with stdout.
- An empty listing prints nothing on stdout and a hint on stderr (`.ifEmpty(...)`).
- Prompts (`askLine`, `askSecret`, `confirm`) write to stderr.

## 3. `--quiet` / `-q`: ids only

Listings print the key column, detail views print their first value (so the first pair of `fields` is the object's key), mutations print the id of every object they created or changed, one per line. A command whose whole point is a one-time secret (`cred issue`, `webhook create`) prints the secret instead of the id, since it is shown only once. Nothing else is printed. This is how a pipeline chains commands:

```sh
id=$(zbox new -q --template dev)
zbox rm -y $(zbox ls -q --status failed)
```

## 4. Errors and exit codes

- Text errors are one line on stderr: `error: <message>`. The API's code goes in brackets (`(sandboxNotFound)`), and the message says what to do next when there is something to do.
- Under `--json` the error is one JSON object on stderr: `{"error":{"code","message","status","meta","exitCode"}}`. `code` is the API's code, `operationFailed` (with the `operation` object), or one of `usage`, `notFound`, `timeout`, `unauthorized`, `unavailable`, `interrupted`, `failed`.
- Raise errors with their class: `usagef` for a wrong call (missing or contradictory flags, malformed values), `notFoundf` for a reference the CLI resolved itself and found nothing for, `notAuthorized` for a missing login or workspace, `unavailable` for a workspace that is not reachable right now and will be (a stopped service, an access renewed too late for the request), and `timedOut` for a wait that ran out. API errors pass through as they come.

| code | meaning | what a caller does |
|---|---|---|
| 0 | ok | |
| 1 | failed (an operation failed, anything unclassified) | read the message |
| 2 | usage (bad flags or arguments, an API 400/422, a missing `--yes`) | fix the call, do not retry |
| 3 | not found | check the id or name |
| 4 | conflict (a state that does not allow the verb, a name taken, an operation in flight) | look at the state, then retry |
| 5 | timed out (`--timeout`, a stopped `exec`, an API 504) | the work may go on, wait on the operation |
| 6 | not authorized (no login, no workspace, a read-only role, a rejected key) | `zbox login`, `zbox setup` or `zbox workspace authorize` |
| 7 | unavailable (502/503, network, `workspaceFull`) | retry later, park a sandbox or scale the workspace |
| 125 | `zbox exec` itself failed (the command did not run) | as above, by the message |
| 130 | interrupted | |

`zbox exec` exits with the command's own exit code, which is why its own failures use 125 (docker's convention) instead of 1 to 7.

## 5. Destructive commands ask

Anything that deletes or drops (`remove`, `base remove|prune`, `template remove`, `snapshot remove`, `file remove`, `webhook remove`, `integration remove`, `quarantine remove`, `connector unset`, `workspace teardown`) does three things:

- It registers `-y/--yes` with `destructive(cmd)`.
- It calls `confirm(action, warning)` before the first change. `action` is a lower-case verb phrase ("terminate sandbox x"), `warning` is what is lost.
- On a terminal it asks. Without one it refuses with exit 2: `pass --yes to terminate sandbox x, stdin is not a terminal to ask on`. An unattended run never destroys by default, and an agent says `--yes` on purpose.

Resolve every reference before asking, so a typo fails before the question and the question names real objects.

Every command that takes several targets goes through `deleteEach(action, warning, labels, verb, del)`: one confirmation naming them all, then one call and one line per target (`x deleted`, or `x terminated in 70 ms (operation y)` when the call answers an operation, so a `--no-wait` run still has the id to wait on), and under `--json` one array of `{id, operation}`. A command that does not confirm (`cred revoke`, `detach`) passes an empty action. The planned extensions are in `TODO.md`.

## 6. Waiting

- Mutations that answer with an operation wait for it by default. `waits(cmd, ...)` registers `--no-wait` (return the operation at once) and `--timeout <duration>` (give up waiting, exit 5).
- Wait through `waitFor(cmd, ws, op)`. On a timeout it says which operation still runs and how to pick it up (`gave up after 5m, wake operation x still runs, `zbox operation wait x` picks it up`), because the work goes on on the workspace. `zbox exec --timeout` is the one timeout that stops the work, and its message says so (`the command was stopped after 30s, its output is in …`). A flag echoed in a message reads as given (`30s`, `5m`), a measured duration goes through `durationString`.
- Waits print one progress note to stderr, never a spinner.

## 7. Idempotent where the intent is a state

A verb whose intent is a target state succeeds when the target is already reached: `park` on a parked sandbox and `wake` on a running one print `x already parked` and exit 0 (`already()` in `cmd/sandbox.go`). Scripts and agents can then ask for the state they want without reading it first. Verbs that create something new (`new`, `fork`, `snapshot create`) are not idempotent.

## 8. Lists and paging

- A list command says what it covers in its help: everything (`zbox ls`), or a default page (`operation ls`: the newest 100).
- When the answer may continue (a full page came back), a stderr note gives the next call: `More may follow: add --before <id> for the next page.`. The flags are always `--limit`, `--before <id>` and, where it makes sense, `--all`. The note is silent under `--json` like every note, so a caller paging with `--json --limit n` continues while the answer holds n rows.
- Filters are repeatable or comma-separated (`--status running,parked`) and case-insensitive.

## 9. References

- A sandbox is named by id or name everywhere it is taken: positional arguments (`sandboxArgs`) and flags (`--sandbox`, `--parent` through `resolveFlag`).
- Bases and templates take a name, a digest or a digest prefix. An ambiguous reference is a usage error that lists the candidates.

## 10. Help

- Commands are spelled out, the primary name is the full word (`list`, `remove`, `rename`, `workspace`, `operation`, `credential`, `snapshot`, `integration`) and the short form is an alias (`ls`, `rm`, `mv`, `ws`, `op`, `cred`, `snap`, `int`; `new` also answers to `create`). Help, examples and hints use the full word, agents and scripts may use either. Everything that manages a workspace is a subcommand of `zbox workspace` (list, switch, authorize, use, forget, status, policy, domain, key, console, teardown); `zbox setup` stays at the top as the convenience that creates one.
- Every command sets `Args` to match its `Use` line: `cobra.NoArgs` when it takes none (a group with a default action too, so an unknown subcommand is refused rather than run as the default), `ExactArgs`, `MinimumNArgs` for a `...` list, `RangeArgs` or `MaximumNArgs` for `[optional]` ones. Without it cobra accepts anything and a typo runs the command on nothing.
- `Short` is one line and starts with a verb. `Long` says what the defaults are (does it wait, is it paged, what it prints).
- Every command an agent is likely to run has an `Example:` block with real invocations, including the `-q` and `--json` forms that chain. Agents copy examples before they read prose.
- The root help (`zbox --help`) carries the output contract and the exit codes, and groups the commands by task (`cmd/groups.go`). A new top-level command joins a group, never the ungrouped "Additional Commands" list.

## 11. Wording

- Text output is short and plain: no jokes, no filler, never softening a cost or a restart.
- The same word is used for the same thing everywhere. A workspace is never a pool, a sandbox is never a VM, and you terminate a sandbox but delete everything else.
- Booleans in tables are `yes`/`no` (`on`/`off` for a switch). Enums are printed as the API spells them, and flags accept any case.

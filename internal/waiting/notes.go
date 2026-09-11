// Package waiting renders the minutes a sandbox pool takes to come up. The wait has no useful output of its own, so it carries the things a new user would otherwise have to go read.
package waiting

// notes are the things worth knowing about a pool, shown while there is nothing else to look at. Keep them true and keep them short, a note that glosses over a cost or a restart
// is worse than no note, the reader finds out the hard way later.
var notes = []string{
	"Sandboxes live in a pool. zbox sets no limit on how many, the pool's CPU, RAM and disk do.",
	"You are billed for the pool's resources, not per sandbox. An idle pool costs the same as a busy one.",
	"You can resize the pool later. It restarts to apply the change, and every sandbox in it stops with it.",
	"The Zerops console shows what is under a pool: its container, logs, metrics and processes.",
	"Add a PostgreSQL service to the project. Every sandbox reaches it over the private network.",
	"A sandbox exposes nothing until you open a port. Each open port gets its own public URL.",
	"`zbox port open <id> <port>` opens a port at any time. Nothing restarts.",
	"A sandbox can run any docker image, `zbox new --image` picks one.",
	"`zbox ls` lists the sandboxes, `zbox shell <id>` opens a shell in one.",
	"`zbox exec <id> -- <command>` runs one command and exits with its exit code.",
	"The web app and the CLI drive the same pool. Use whichever is closer.",
	"Everything here is a plain HTTP API. The pool serves its OpenAPI docs at /api/rest/swagger/.",
	"The default image is being baked into the pool now. Every sandbox after this starts in seconds.",
	"`zbox teardown` deletes the whole Zerops project: the pool, the sandboxes, everything in it.",
}

// Notes is the rotation shown during a wait.
func Notes() []string {
	return notes
}

// Package waiting renders the minutes a workspace takes to come up. The wait has no useful output of its own, so it carries the things a new user would otherwise have to go read.
package waiting

import "strings"

// notes are the things worth knowing about a workspace, shown while there is nothing else to look at. Keep them true and keep them short, a note that glosses over a cost or a restart
// is worse than no note, the reader finds out the hard way later.
var notes = []string{
	"Sandboxes live in a workspace. zbox sets no limit on how many, the workspace's CPU, RAM and disk do.",
	"You are billed for the workspace's resources, not per sandbox. An idle workspace costs the same as a busy one.",
	"`zbox workspace resize` changes the workspace's CPU, RAM or disk later. The VM restarts at once, every running sandbox is parked first.",
	"The Zerops GUI shows what is under a workspace: its container, logs, metrics and processes.",
	"Add a PostgreSQL service to the project. A sandbox in project mode reaches it over the private network.",
	"A sandbox exposes nothing until you declare a port. Each port gets its own URL, {port}-{id}-{ingress-host}.",
	"`zbox port open <id> <port>` declares a port at any time. Nothing restarts.",
	"A port is guarded by the workspace API key unless you say otherwise: 3000/public opens it to everyone.",
	"`zbox park <id>` keeps a sandbox on disk at zero RAM. A request to one of its ports wakes it.",
	"`zbox fork <id>` continues a running sandbox in a new one, processes and all.",
	"A sandbox boots from a base, any OCI image. `zbox base add <image>` pulls one.",
	"`zbox ls` lists the sandboxes, `zbox shell <id>` opens a shell in one.",
	"`zbox exec <id> -- <command>` runs one command and exits with its exit code.",
	"Every command takes --json. Scripts and agents get the API objects, not the text.",
	"Zerops Box ({box}) and the CLI drive the same workspace. Use whichever is closer.",
	"The default base is being added to the workspace now. Every sandbox after this starts in seconds.",
	"`zbox workspace teardown` deletes the workspace service with every sandbox, base and snapshot in it, the Zerops project stays.",
}

// Notes is the rotation shown during a wait, with {box} replaced by the host of Zerops Box that `zbox workspace console` opens.
func Notes(boxHost string) []string {
	out := make([]string, len(notes))
	for i, n := range notes {
		out[i] = strings.ReplaceAll(n, "{box}", boxHost)
	}
	return out
}

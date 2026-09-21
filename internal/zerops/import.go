package zerops

import (
	"bytes"
	"text/template"
)

// The workspace is a Sandbox Workspace service (zbox@1): an Ubuntu VM with the orchestrator mounted at /opt/zerops/bin/zerops-zbox-orchestrator as a zstart package, so nothing is downloaded or deployed and a new orchestrator build reaches the workspace with the platform. The orchestrator runs Firecracker guests inside that VM (ZBOX_SUBSTRATE_KIND), there is no docker.
// One container, two dedicated cpus, 8 GB of RAM and a 50 GB disk, pinned so the workspace never scales: the orchestrator holds the sqlite file and the guests, a second container or a resize would not carry them.
// Two http ports with subdomain access give two hosts in zeropsSubdomain: the API host first, the ingress host second. The orchestrator reads both itself.
// The workspace API key is the service env ZBOX_WORKSPACE_API_KEY, a sensitive system env the Sandbox Workspace controller generates at service create (like the Qdrant apiKey), so the import carries no key of its own; the orchestrator reads it from its environment.
// The orchestrator needs root on this type (network namespaces, taps, nftables, the jailer) and elevates itself: `run` re-execs under sudo with an explicit env list when ZBOX_SUBSTRATE_KIND is FIRECRACKER. The image ships sudo-rs, which ignores `sudo -E`, so the template must not use it or the orchestrator never sees apiKey. `migrate` runs as zerops on the zerops-owned /var/lib/zbox, which the image mounts itself.
// The orchestrator does not migrate its database at start: `migrate` creates the sqlite file when missing and applies the pending migrations, exiting non-zero on failure so a broken schema never reaches `run`.
// An empty ProjectName drops the project section, which is the shape the service-stack import into an existing project expects.
// zeropsSetup is spelled out because the hostname is free to differ from the setup name, and without it Zerops would look for a setup named after the hostname.
const importTemplate = `{{with .ProjectName}}project:
  name: {{.}}
{{end}}services:
  - hostname: {{.Hostname}}
    type: zbox@1
    enableSubdomainAccess: true
    zeropsSetup: zbox
    minContainers: 1
    maxContainers: 1
    verticalAutoscaling:
      cpuMode: DEDICATED
      minCpu: 2
      maxCpu: 2
      minRam: 8
      maxRam: 8
      minDisk: 50
      maxDisk: 50
    zeropsYaml:
      zerops:
        - setup: zbox
          run:
            base: zbox@1
            ports:
              - port: 8080
                httpSupport: true
              - port: 8081
                httpSupport: true
            envVariables:
              ZBOX_SUBSTRATE_KIND: FIRECRACKER
            initCommands:
              - /opt/zerops/bin/zerops-zbox-orchestrator migrate
            start: /opt/zerops/bin/zerops-zbox-orchestrator run
`

type ImportParams struct {
	ProjectName string
	Hostname    string
}

func RenderImport(p ImportParams) (string, error) {
	t, err := template.New("import").Parse(importTemplate)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, p); err != nil {
		return "", err
	}
	return buf.String(), nil
}

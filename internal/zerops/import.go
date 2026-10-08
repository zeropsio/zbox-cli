package zerops

import (
	"bytes"
	"fmt"
	"regexp"
	"text/template"
)

// The workspace is a Sandbox Workspace service (zbox@1), a managed (STANDARD) service type since 2026-10-06: an Ubuntu VM in which zsc runs the orchestrator from the image as root (a systemd unit that runs `zerops-zbox-orchestrator prepare` and then `run`), so the import carries no appVersion, no zerops.yml, no commands and nothing is deployed; a new orchestrator build reaches the workspace with the platform. The orchestrator runs Firecracker guests inside that VM, its only substrate, there is no docker.
// One container of Cpu dedicated cpus, Ram GB of RAM and a Disk GB disk, min equal to max so the workspace never scales: the orchestrator holds the sqlite file and the guests, a second container or a resize would not carry them. The defaults live in cmd/setup.go (24 cpu, 128 GB, 250 GB since 2026-09-22, sized for the alpha metal nodes), `--cpu`, `--ram` and `--disk` go smaller. The type takes no minContainers or maxContainers, the import refuses them.
// The controller publishes the two http ports itself (8080 API, 8081 ingress) and writes zeropsSubdomain as two lines, the API host first.
// The workspace API key is the service env ZBOX_WORKSPACE_API_KEY, a sensitive system env the controller generates at service create (like the Qdrant apiKey), so the import carries no key of its own. The orchestrator reads no service env: before every start zsc writes the two keys (API, data volume), the two hosts and the bases into /etc/zerops-zbox-orchestrator/config.yml, the only configuration the orchestrator reads.
// The bases come from the plain user env ZBOX_WORKSPACE_BASES: image references or `docker save` tarball URLs separated by whitespace or commas. `run` pulls each in the background once the API listens (a failed pull is logged and tried again at the next start, a base registered earlier under the same reference is kept), so a slow registry never gates the service start. The platform has no default, so the import sets the first base; an edit of the env or of the key applies at the next explicit restart, stop and start, or reload of the service (reload keeps the sandboxes up, a restart reboots the VM), never on its own. Templates are not built at start, cmd/setup.go creates them through the API once the base is READY.
// An empty ProjectName drops the project section, which is the shape the service-stack import into an existing project expects.
const importTemplate = `{{with .ProjectName}}project:
  name: {{.}}
{{end}}services:
  - hostname: {{.Hostname}}
    type: zbox@1
    enableSubdomainAccess: true
    verticalAutoscaling:
      cpuMode: DEDICATED
      minCpu: {{.Cpu}}
      maxCpu: {{.Cpu}}
      minRam: {{.Ram}}
      maxRam: {{.Ram}}
      minDisk: {{.Disk}}
      maxDisk: {{.Disk}}
    envVariables:
      ZBOX_WORKSPACE_BASES: {{.Image}}
`

type ImportParams struct {
	ProjectName string
	Hostname    string
	// Cpu, Ram and Disk size the one container: dedicated cpus, GB of RAM, GB of disk, each pinned as min and max. The driver floor is 2 cpu, 2 GB, 10 GB.
	Cpu  int
	Ram  int
	Disk int
	// Image is the first base of the workspace, a registry reference or the URL of a `docker save` tarball, the value of ZBOX_WORKSPACE_BASES.
	Image string
}

// The image lands as a plain scalar in the YAML and is split on whitespace and commas by the orchestrator, so it is held to what neither reads anything into.
var importImage = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+=-]*$`)

func RenderImport(p ImportParams) (string, error) {
	if p.Cpu < 2 || p.Ram < 2 || p.Disk < 10 {
		return "", fmt.Errorf("workspace size %d cpu, %d GB RAM, %d GB disk is under the floor of 2 cpu, 2 GB RAM, 10 GB disk", p.Cpu, p.Ram, p.Disk)
	}
	if !importImage.MatchString(p.Image) {
		return "", fmt.Errorf("image %q cannot go into the workspace yaml, use a plain registry reference or URL", p.Image)
	}
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

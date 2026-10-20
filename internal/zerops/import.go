package zerops

import (
	"bytes"
	"fmt"
	"text/template"
)

// The workspace is a Box Workspace service (zbox@1), a managed (STANDARD) service type: an Ubuntu VM in which zsc runs the orchestrator from the image as root (a systemd unit that runs `zerops-zbox-orchestrator prepare` and then `run`), so the import carries no appVersion, no zerops.yml, no commands and nothing is deployed; a new orchestrator build reaches the workspace with the platform. The orchestrator runs Firecracker guests inside that VM, its only substrate, there is no docker.
// One container of Cpu dedicated cpus, Ram GiB of RAM and a Disk GiB disk, min equal to max so the workspace never scales: the orchestrator holds the sqlite file and the guests, a second container or a resize would not carry them. The CLI sends no size of its own, the platform's defaults for the type apply (2 cpu, 4 GiB, 10 GiB) and `--cpu`, `--ram` and `--disk` pin what they name. The type takes no minContainers or maxContainers, the import refuses them.
// The controller publishes the two http ports itself (8080 API, 8081 ingress) and writes zeropsSubdomain as two lines, the API host first.
// The workspace API key is the service env ZBOX_WORKSPACE_API_KEY, a sensitive system env the controller generates at service create (like the Qdrant apiKey), so the import carries no key of its own. The orchestrator reads no service env: before every start zsc writes the two keys (API, data volume) into /etc/zerops-zbox-orchestrator/config.yml, the only configuration the orchestrator reads; domains and bases are set through its API, the platform sets only the secrets.
// The import names no base: cmd/setup.go registers every base through the API once the orchestrator answers (builds, then pulls) and creates the templates on them.
// Subdomain access publishes the two ports on the edge (the API host, and the ingress host that cmd/setup.go adds as the workspace's first domain); the CLI reaches the API through zproxy and does not need it, the sandbox port URLs, `zbox open` and the public and members ports do, so it stays on unless NoSubdomain (`zbox setup --no-subdomain`) leaves the line out.
// An empty ProjectName drops the project section, which is the shape the service-stack import into an existing project expects.
const importTemplate = `{{with .ProjectName}}project:
  name: {{.}}
{{end}}services:
  - hostname: {{.Hostname}}
    type: zbox@1
{{- if not .NoSubdomain}}
    enableSubdomainAccess: true
{{- end}}
{{- if .Sized}}
    verticalAutoscaling:
{{- if .Cpu}}
      cpuMode: DEDICATED
      minCpu: {{.Cpu}}
      maxCpu: {{.Cpu}}
{{- end}}
{{- if .Ram}}
      minRam: {{.Ram}}
      maxRam: {{.Ram}}
{{- end}}
{{- if .Disk}}
      minDisk: {{.Disk}}
      maxDisk: {{.Disk}}
{{- end}}
{{- end}}
`

type ImportParams struct {
	ProjectName string
	Hostname    string
	// Cpu, Ram and Disk size the one container: dedicated cpus, GiB of RAM, GiB of disk, each pinned as min and max. Zero leaves the resource to the
	// platform's default for the type, the driver floor is 2 cpu, 2 GiB, 10 GiB.
	Cpu  int
	Ram  int
	Disk int
	// NoSubdomain leaves subdomain access off: no API host and no ingress host, so sandbox ports get no public URL until a domain is added. The API is reached through zproxy either way.
	NoSubdomain bool
}

// Sized tells whether any resource is pinned, so the template leaves the autoscaling block out otherwise.
func (p ImportParams) Sized() bool {
	return p.Cpu > 0 || p.Ram > 0 || p.Disk > 0
}

func RenderImport(p ImportParams) (string, error) {
	if p.Cpu < 0 || p.Ram < 0 || p.Disk < 0 || (p.Cpu > 0 && p.Cpu < 2) || (p.Ram > 0 && p.Ram < 2) || (p.Disk > 0 && p.Disk < 10) {
		return "", fmt.Errorf("workspace size %d cpu, %d GiB RAM, %d GiB disk is under the floor of 2 cpu, 2 GiB RAM, 10 GiB disk", p.Cpu, p.Ram, p.Disk)
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

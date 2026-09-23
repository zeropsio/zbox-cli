package zerops

import (
	"bytes"
	"fmt"
	"regexp"
	"text/template"
)

// The workspace is a Sandbox Workspace service (zbox@1): an Ubuntu VM with the orchestrator mounted at /opt/zerops/bin/zerops-zbox-orchestrator as a zstart package, so nothing is downloaded or deployed and a new orchestrator build reaches the workspace with the platform. The orchestrator runs Firecracker guests inside that VM, its only substrate, there is no docker.
// One container of Cpu dedicated cpus, Ram GB of RAM and a Disk GB disk, min equal to max so the workspace never scales: the orchestrator holds the sqlite file and the guests, a second container or a resize would not carry them. The defaults live in cmd/setup.go (24 cpu, 128 GB, 250 GB since 2026-09-22, sized for the alpha metal nodes), `--cpu`, `--ram` and `--disk` go smaller.
// Two http ports with subdomain access give two hosts in zeropsSubdomain: the API host first, the ingress host second. The orchestrator reads both itself.
// The workspace API key is the service env ZBOX_WORKSPACE_API_KEY, a sensitive system env the Sandbox Workspace controller generates at service create (like the Qdrant apiKey), so the import carries no key of its own; the orchestrator reads it from its environment.
// The orchestrator needs root on this type (network namespaces, taps, nftables, the jailer) and elevates itself: every subcommand re-execs under sudo with an explicit env list when it is not root. The image ships sudo-rs, which ignores `sudo -E`, so the template must not use it or the orchestrator never sees its key. The image mounts /var/lib/zbox itself.
// The orchestrator does not migrate its database at start: `migrate` creates the sqlite file when missing and applies the pending migrations, exiting non-zero on failure so a broken schema never reaches `run`.
// `run` does not prepare the host either: `host prepare` does (guest kernel, rw disk, cgroups, nft base table) and leaves a stamp without which `run` refuses to start, so the line is mandatory and comes right after `migrate`. `base ensure` and `template ensure` are the init-time twins of POST /base and POST /template, idempotent, so the workspace has its base (and the asked-for warm templates, each waited for) before the API answers. All of them elevate themselves like `run`, `migrate` included, so /var/lib/zbox has one owner.
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
      minCpu: {{.Cpu}}
      maxCpu: {{.Cpu}}
      minRam: {{.Ram}}
      maxRam: {{.Ram}}
      minDisk: {{.Disk}}
      maxDisk: {{.Disk}}
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
            initCommands:
              - /opt/zerops/bin/zerops-zbox-orchestrator migrate
              - /opt/zerops/bin/zerops-zbox-orchestrator host prepare
              - /opt/zerops/bin/zerops-zbox-orchestrator base ensure {{.Image}}
{{- range .Templates}}
              - /opt/zerops/bin/zerops-zbox-orchestrator template ensure --name {{.Name}} --base {{$.Image}}{{if .MemoryBytes}} --memory {{.MemoryBytes}}{{end}}{{if .Cpus}} --cpus {{.Cpus}}{{end}}
{{- end}}
            start: /opt/zerops/bin/zerops-zbox-orchestrator run
`

type ImportParams struct {
	ProjectName string
	Hostname    string
	// Cpu, Ram and Disk size the one container: dedicated cpus, GB of RAM, GB of disk, each pinned as min and max. The driver floor is 2 cpu, 2 GB, 10 GB.
	Cpu  int
	Ram  int
	Disk int
	// Image is the first base of the workspace, a registry reference or the URL of a `docker save` tarball.
	Image string
	// Templates are warmed from Image before the orchestrator starts.
	Templates []ImportTemplate
}

// ImportTemplate is one `template ensure` line. Zero MemoryBytes or Cpus leave the sandbox default.
type ImportTemplate struct {
	Name        string
	MemoryBytes int64
	Cpus        int
}

// The values land unquoted in a command line inside the YAML, so they are held to what neither YAML nor a shell reads anything into.
var (
	importImage = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+=-]*$`)
	importName  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

func RenderImport(p ImportParams) (string, error) {
	if p.Cpu < 2 || p.Ram < 2 || p.Disk < 10 {
		return "", fmt.Errorf("workspace size %d cpu, %d GB RAM, %d GB disk is under the floor of 2 cpu, 2 GB RAM, 10 GB disk", p.Cpu, p.Ram, p.Disk)
	}
	if !importImage.MatchString(p.Image) {
		return "", fmt.Errorf("image %q cannot go into the workspace yaml, use a plain registry reference or URL", p.Image)
	}
	for _, t := range p.Templates {
		if !importName.MatchString(t.Name) {
			return "", fmt.Errorf("template name %q cannot go into the workspace yaml, use letters, digits, dots, dashes and underscores", t.Name)
		}
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

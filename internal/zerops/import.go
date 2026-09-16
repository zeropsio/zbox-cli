package zerops

import (
	"bytes"
	"text/template"
)

// The orchestrator is a zstart package mounted into every docker VM at /opt/zerops/bin/zerops-zbox-orchestrator, so nothing is downloaded or deployed: a new orchestrator build reaches the workspace with the platform.
// The default sandbox image is loaded from a saved tarball as an init command rather than pulled by the orchestrator, so the service reaching ACTIVE can serve `POST /base zbox-base:latest` right away. The load is skipped when the image is already there, so a restart does not fetch the tarball again.
// Two http ports with subdomain access give two hosts in zeropsSubdomain: the API host first, the ingress host second. The orchestrator reads both itself.
// The workspace API key is a generated secret env of the service (apiKey), which the orchestrator reads from its own environment. It is not mapped in the start line: Zerops runs the start line through exec, so a leading assignment is taken for the program.
// The service runs as user zerops with only /var/www writable, and the orchestrator keeps its sqlite file and session outputs under /var/lib/zbox, hence the first init command.
// The orchestrator does not migrate its database at start: `zbox migrate` creates the sqlite file when missing and applies the pending migrations, exiting non-zero on failure so a broken schema never reaches `run`.
// An empty ProjectName drops the project section, which is the shape the service-stack import into an existing project expects.
// zeropsSetup is spelled out because the hostname is free to differ from the setup name, and without it Zerops would look for a setup named after the hostname.
const importTemplate = `{{with .ProjectName}}project:
  name: {{.}}
{{end}}services:
  - hostname: {{.Hostname}}
    type: docker@26.1
    enableSubdomainAccess: true
    zeropsSetup: zbox
    envSecrets:
      apiKey: <@generateRandomString(<32>)>
    verticalAutoscaling:
      cpuMode: DEDICATED
      minCpu: 1
      maxCpu: 1
      minRam: 8
      maxRam: 8
      minDisk: 20
      maxDisk: 20
    zeropsYaml:
      zerops:
        - setup: zbox
          run:
            ports:
              - port: 8080
                httpSupport: true
              - port: 8081
                httpSupport: true
            initCommands:
              - sudo mkdir -p /var/lib/zbox && sudo chown zerops:zerops /var/lib/zbox
              - /opt/zerops/bin/zerops-zbox-orchestrator migrate
              - docker image inspect zbox-base:latest >/dev/null 2>&1 || ((command -v curl >/dev/null && curl -fsSL {{.ImageUrl}}) || wget -qO- {{.ImageUrl}}) | docker load
            start: /opt/zerops/bin/zerops-zbox-orchestrator run
`

type ImportParams struct {
	ProjectName string
	Hostname    string
	ImageUrl    string
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

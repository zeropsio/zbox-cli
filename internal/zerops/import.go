package zerops

import (
	"bytes"
	"text/template"
)

// The docker service downloads the orchestrator binary on every start, so no code deploy is needed. The download lands in a temp file that is renamed over the path: on a restart /var/www/zbox is the running binary and writing onto it fails (curl exit 23, wget "Text file busy"), a rename works.
// The default sandbox image is loaded from a saved tarball as an init command rather than pulled by the orchestrator, so the service reaching ACTIVE can serve `POST /base zbox-base:latest` right away.
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
              - (command -v curl >/dev/null && curl -fsSL -o /var/www/zbox.new {{.BinaryUrl}}) || wget -qO /var/www/zbox.new {{.BinaryUrl}}
              - chmod +x /var/www/zbox.new && mv -f /var/www/zbox.new /var/www/zbox
              - /var/www/zbox migrate
              - ((command -v curl >/dev/null && curl -fsSL {{.ImageUrl}}) || wget -qO- {{.ImageUrl}}) | docker load
            start: /var/www/zbox run
`

type ImportParams struct {
	ProjectName string
	Hostname    string
	BinaryUrl   string
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

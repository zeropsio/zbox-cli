package zerops

import (
	"bytes"
	"text/template"
)

// The docker service downloads the orchestrator binary on every start, so no code deploy is needed.
// prepare-image is an init command rather than something the orchestrator does in the background, a service that reaches ACTIVE has to be able to serve `zbox new` right away.
// The zbox port gets a Zerops subdomain, the orchestrator builds sandbox URLs from the request host.
// An empty ProjectName drops the project section, which is the shape the service-stack import into an existing project expects.
// zeropsSetup is spelled out because the hostname is free to differ from the setup name, and without it Zerops would look for a setup named after the hostname.
const importTemplate = `{{with .ProjectName}}project:
  name: {{.}}
{{end}}services:
  - hostname: {{.Hostname}}
    type: docker@26.1
    enableSubdomainAccess: true
    zeropsSetup: zbox
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
            initCommands:
              - (command -v curl >/dev/null && curl -fsSL -o /var/www/zbox {{.BinaryUrl}}) || wget -qO /var/www/zbox {{.BinaryUrl}}
              - chmod +x /var/www/zbox
              - /var/www/zbox prepare-image
            start: /var/www/zbox run
`

type ImportParams struct {
	ProjectName string
	Hostname    string
	BinaryUrl   string
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

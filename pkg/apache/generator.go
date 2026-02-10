package apache

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"go-local-server/internal/config"
	"go-local-server/internal/projects"
)

const vhostTemplate = `<VirtualHost *:80>
    ServerName {{.Domain}}
    ServerAlias *.{{.Domain}}

    DocumentRoot "{{.ProjectPath}}"

    <Directory "{{.ProjectPath}}">
        Options Indexes FollowSymLinks
        AllowOverride All
        Require all granted
    </Directory>

    ErrorLog "/var/log/apache2/{{.ProjectID}}-error.log"
    CustomLog "/var/log/apache2/{{.ProjectID}}-access.log" combined
</VirtualHost>
`

type VhostData struct {
	ProjectID   string
	Domain      string
	ProjectPath string
}

type Generator struct {
	config *config.AppConfig
}

func NewGenerator(cfg *config.AppConfig) *Generator {
	return &Generator{config: cfg}
}

func (g *Generator) GenerateVhost(project *projects.Project) error {
	docRoot := project.Path

	// Use custom DocumentRoot if set
	if project.DocumentRoot != "" {
		docRoot = filepath.Join(project.Path, project.DocumentRoot)
	} else if _, err := os.Stat(filepath.Join(project.Path, "public", "index.php")); err == nil {
		// Auto-detect MVC public folder
		docRoot = filepath.Join(project.Path, "public")
	}

	data := VhostData{
		ProjectID:   project.ID,
		Domain:      project.Domain,
		ProjectPath: docRoot,
	}

	tmpl, err := template.New("vhost").Parse(vhostTemplate)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return err
	}

	// Docker stack reads from repo ./apache/sites, but also keep a copy in app support
	// so projects remain portable.
	vhostPath := filepath.Join(config.ConfigDir, "apache", "sites", project.ID+".conf")
	if err := os.MkdirAll(filepath.Dir(vhostPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(vhostPath, buf.Bytes(), 0644)
}

func (g *Generator) GenerateAllVhosts() error {
	pm := projects.NewManager(g.config)
	projectList, err := pm.List()
	if err != nil {
		return err
	}

	for _, project := range projectList {
		if err := g.GenerateVhost(project); err != nil {
			fmt.Printf("Error generating apache vhost for %s: %v\n", project.Name, err)
		}
	}
	return nil
}

func (g *Generator) RemoveVhost(projectID string) {
	vhostPath := filepath.Join(config.ConfigDir, "apache", "sites", projectID+".conf")
	os.Remove(vhostPath)
}

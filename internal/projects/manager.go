package projects

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go-local-server/internal/config"
)

type DatabaseConfig struct {
	DBName     string `json:"db_name"`
	DBUser     string `json:"db_user"`
	DBPassword string `json:"db_password"`
	DBHost     string `json:"db_host"`
	DBPort     int    `json:"db_port"`
}

type Project struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Domain         string         `json:"domain"`
	Path           string         `json:"path"`
	PHPVersion     string         `json:"php_version"`
	Database       DatabaseConfig `json:"database"`
	HasPHPMyAdmin  bool           `json:"has_phpmyadmin"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	IsActive       bool           `json:"is_active"`
}

type Manager struct {
	projectsDir string
	config      *config.AppConfig
}

func NewManager(cfg *config.AppConfig) *Manager {
	return &Manager{
		projectsDir: config.ProjectsDir,
		config:      cfg,
	}
}

func (m *Manager) Create(name, path, phpVersion string, dbConfig DatabaseConfig) (*Project, error) {
	id := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	domain := fmt.Sprintf("%s.%s", id, m.config.Domain)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("project path does not exist: %s", path)
	}

	// Set default DB host/port if not provided
	if dbConfig.DBHost == "" {
		dbConfig.DBHost = "127.0.0.1"
	}
	if dbConfig.DBPort == 0 {
		dbConfig.DBPort = m.config.MySQLPort
	}
	if dbConfig.DBName == "" {
		dbConfig.DBName = id
	}

	project := &Project{
		ID:             id,
		Name:           name,
		Domain:         domain,
		Path:           path,
		PHPVersion:     phpVersion,
		Database:       dbConfig,
		HasPHPMyAdmin:  true, // Default to having phpMyAdmin
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
		IsActive:       true,
	}

	if err := m.Save(project); err != nil {
		return nil, err
	}

	return project, nil
}

func (m *Manager) CreateWithSubdomain(name, subdomain, path, phpVersion string, dbConfig DatabaseConfig) (*Project, error) {
	id := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	domain := fmt.Sprintf("%s.%s", subdomain, m.config.Domain)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("project path does not exist: %s", path)
	}

	// Set default DB host/port if not provided
	if dbConfig.DBHost == "" {
		dbConfig.DBHost = "127.0.0.1"
	}
	if dbConfig.DBPort == 0 {
		dbConfig.DBPort = m.config.MySQLPort
	}
	if dbConfig.DBName == "" {
		dbConfig.DBName = subdomain
	}

	project := &Project{
		ID:             id,
		Name:           name,
		Domain:         domain,
		Path:           path,
		PHPVersion:     phpVersion,
		Database:       dbConfig,
		HasPHPMyAdmin:  true, // Default to having phpMyAdmin
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
		IsActive:       true,
	}

	if err := m.Save(project); err != nil {
		return nil, err
	}

	return project, nil
}

func (m *Manager) Save(project *Project) error {
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		return err
	}

	filePath := filepath.Join(m.projectsDir, project.ID+".json")
	return os.WriteFile(filePath, data, 0644)
}

func (m *Manager) Load(id string) (*Project, error) {
	filePath := filepath.Join(m.projectsDir, id+".json")
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var project Project
	if err := json.Unmarshal(data, &project); err != nil {
		return nil, err
	}

	return &project, nil
}

func (m *Manager) List() ([]*Project, error) {
	entries, err := os.ReadDir(m.projectsDir)
	if err != nil {
		return nil, err
	}

	var projects []*Project
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		id := strings.TrimSuffix(entry.Name(), ".json")
		project, err := m.Load(id)
		if err != nil {
			continue
		}

		projects = append(projects, project)
	}

	return projects, nil
}

func (m *Manager) Delete(id string) error {
	filePath := filepath.Join(m.projectsDir, id+".json")
	return os.Remove(filePath)
}

func (m *Manager) Update(project *Project) error {
	project.UpdatedAt = time.Now()
	return m.Save(project)
}

func (m *Manager) GetByDomain(domain string) (*Project, error) {
	projectList, err := m.List()
	if err != nil {
		return nil, err
	}

	for _, p := range projectList {
		if p.Domain == domain || strings.HasSuffix(domain, "."+p.Domain) {
			return p, nil
		}
	}

	return nil, fmt.Errorf("project not found for domain: %s", domain)
}

// GeneratePHPIndex creates a simple PHP index file for the project
func (m *Manager) GeneratePHPIndex(project *Project) error {
	indexPath := filepath.Join(project.Path, "index.php")

	// Don't overwrite existing index.php
	if _, err := os.Stat(indexPath); err == nil {
		return nil
	}

	phpTemplate := `<?php
$project = %q;
$domain = %q;
$phpVersion = %q;
?>
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title><?php echo htmlspecialchars($project); ?></title>
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; margin: 40px; }
        .box { max-width: 720px; }
        .row { margin: 10px 0; }
        .label { display: inline-block; width: 120px; color: #666; }
        a { color: #2563eb; text-decoration: none; }
        a:hover { text-decoration: underline; }
        code { background: #f3f4f6; padding: 2px 6px; border-radius: 6px; }
    </style>
</head>
<body>
    <div class="box">
        <h1><?php echo htmlspecialchars($project); ?></h1>
        <div class="row"><span class="label">Domain</span><code><?php echo htmlspecialchars($domain); ?></code></div>
        <div class="row"><span class="label">PHP</span><code><?php echo htmlspecialchars($phpVersion); ?></code></div>
        <div class="row"><a href="phpinfo.php">phpinfo()</a></div>
    </div>
</body>
</html>
`

	content := fmt.Sprintf(phpTemplate, project.Name, project.Domain, project.PHPVersion)

	return os.WriteFile(indexPath, []byte(content), 0644)
}

// GeneratePHPInfo creates a phpinfo file

func (m *Manager) GeneratePHPInfo(project *Project) error {
	phpinfoPath := filepath.Join(project.Path, "phpinfo.php")

	// Don't overwrite existing file
	if _, err := os.Stat(phpinfoPath); err == nil {
		return nil
	}

	content := `<?php
/**
 * PHP Info
 */
phpinfo();
?>`

	return os.WriteFile(phpinfoPath, []byte(content), 0644)
}

func (m *Manager) findMVCTemplateDir() (string, error) {
	// Prefer running from repo root
	cwd, _ := os.Getwd()
	candidate := filepath.Join(cwd, "pkg", "php-mvc-main")
	if st, err := os.Stat(candidate); err == nil && st.IsDir() {
		return candidate, nil
	}

	// Fallback to executable location
	exe, err := os.Executable()
	if err == nil {
		exeDir := filepath.Dir(exe)
		candidate = filepath.Join(exeDir, "..", "pkg", "php-mvc-main")
		candidate = filepath.Clean(candidate)
		if st, err2 := os.Stat(candidate); err2 == nil && st.IsDir() {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("mvc template not found (expected pkg/php-mvc-main)")
}

func copyFileNoOverwrite(srcPath, dstPath string, mode fs.FileMode) error {
	if _, err := os.Stat(dstPath); err == nil {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return err
	}

	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)
	return err
}

func (m *Manager) CopyMVCTemplate(project *Project) error {
	srcRoot, err := m.findMVCTemplateDir()
	if err != nil {
		return err
	}

	return filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.Name() == ".DS_Store" {
			return nil
		}

		dstPath := filepath.Join(project.Path, rel)
		if d.IsDir() {
			return os.MkdirAll(dstPath, 0755)
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFileNoOverwrite(path, dstPath, info.Mode())
	})
}

// GenerateDBConfig creates a database configuration file for the project
func (m *Manager) GenerateDBConfig(project *Project) error {
	configPath := filepath.Join(project.Path, "db_config.php")

	// Don't overwrite existing file
	if _, err := os.Stat(configPath); err == nil {
		return nil
	}

	phpTemplate := `<?php
/**
 * Database Configuration
 * Auto-generated by Go Local Server
 */

return [
    'host'     => '%s',
    'port'     => %d,
    'database' => '%s',
    'username' => '%s',
    'password' => '%s',
    'charset'  => 'utf8mb4',
    'collation' => 'utf8mb4_unicode_ci',
];
`

	content := fmt.Sprintf(phpTemplate,
		project.Database.DBHost,
		project.Database.DBPort,
		project.Database.DBName,
		project.Database.DBUser,
		project.Database.DBPassword,
	)

	return os.WriteFile(configPath, []byte(content), 0644)
}

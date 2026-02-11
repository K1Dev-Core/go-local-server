package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const AppName = "GoLocalServer"
const AppVersion = "1.0.0"

var ConfigDir string
var ConfigFile string
var ProjectsDir string
var LogDir string

func init() {
	home, _ := os.UserHomeDir()
	ConfigDir = filepath.Join(home, "Library", "Application Support", "GoLocalServer")
	ProjectsDir = filepath.Join(ConfigDir, "projects")
	LogDir = filepath.Join(ConfigDir, "logs")
	ConfigFile = filepath.Join(ConfigDir, "config.json")
}

type AppConfig struct {
	NginxPath       string `json:"nginx_path"`
	PHPPath         string `json:"php_path"`
	MySQLPath       string `json:"mysql_path"`
	DNSPort         int    `json:"dns_port"`
	HTTPPort        int    `json:"http_port"`
	HTTPSPort       int    `json:"https_port"`
	MySQLPort       int    `json:"mysql_port"`
	Domain          string `json:"domain"`
	PreferredEditor string `json:"preferred_editor"` // Cursor, Windsurf, or VSCode
}

func DefaultConfig() *AppConfig {
	return &AppConfig{
		NginxPath:       "/opt/homebrew/opt/nginx/bin/nginx",
		PHPPath:         "/opt/homebrew/opt/php/sbin/php-fpm",
		MySQLPath:       "/opt/homebrew/opt/mysql/bin/mysqld",
		DNSPort:         1053,
		HTTPPort:        80,
		HTTPSPort:       443,
		MySQLPort:       3306,
		Domain:          "localhost",
		PreferredEditor: "VSCode",
	}
}

func (c *AppConfig) Load() error {
	data, err := os.ReadFile(ConfigFile)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, c)
}

func (c *AppConfig) Save() error {
	os.MkdirAll(ConfigDir, 0755)
	data, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(ConfigFile, data, 0644)
}

func (c *AppConfig) GetEditorInfo() (appName, displayName string) {
	switch c.PreferredEditor {
	case "Cursor":
		return "Cursor", "Cursor"
	case "Windsurf":
		return "Windsurf", "Windsurf"
	case "VSCode":
		return "Visual Studio Code", "VS Code"
	default:
		return "Visual Studio Code", "VS Code"
	}
}

func EnsureDirs() {
	os.MkdirAll(ConfigDir, 0755)
	os.MkdirAll(ProjectsDir, 0755)
	os.MkdirAll(LogDir, 0755)
}

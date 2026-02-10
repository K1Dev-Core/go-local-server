package services

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go-local-server/internal/config"
	"go-local-server/pkg/apache"
)

func findDockerBinary() string {
	locations := []string{
		"/usr/local/bin/docker",
		"/opt/homebrew/bin/docker",
		"/usr/bin/docker",
		"/Applications/Docker.app/Contents/Resources/bin/docker",
		"/Applications/Docker.app/Contents/MacOS/docker",
		"/usr/local/docker/bin/docker",
	}

	if home, err := os.UserHomeDir(); err == nil {
		locations = append(locations,
			filepath.Join(home, ".docker", "bin", "docker"),
			filepath.Join(home, "bin", "docker"),
		)
	}

	for _, loc := range locations {
		if st, err := os.Stat(loc); err == nil && !st.IsDir() {
			return loc
		}
	}

	return "docker"
}

// DockerServiceManager uses Docker Compose to manage services
type DockerServiceManager struct {
	Config       *config.AppConfig
	Services     map[string]*Service
	composeFile  string
}

func NewDockerServiceManager(cfg *config.AppConfig) *DockerServiceManager {
	dsm := &DockerServiceManager{
		Config:      cfg,
		Services:    make(map[string]*Service),
		composeFile: filepath.Join(config.ConfigDir, "..", "docker-compose.yml"),
	}

	// Prefer the app-copied docker resources directory (works when running from .app)
	candidate := filepath.Join(config.ConfigDir, "docker", "docker-compose.yml")
	if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
		dsm.composeFile = candidate
	} else {
		// Fallback to local project directory for docker-compose
		cwd, _ := os.Getwd()
		dsm.composeFile = filepath.Join(cwd, "docker-compose.yml")
	}

	dsm.Services["nginx"] = &Service{
		Name:    "apache",
		Status:  StatusStopped,
		LogFile: filepath.Join(config.LogDir, "apache.log"),
	}

	dsm.Services["php-fpm"] = &Service{
		Name:    "php",
		Status:  StatusStopped,
		LogFile: filepath.Join(config.LogDir, "php-fpm.log"),
	}

	dsm.Services["mysql"] = &Service{
		Name:    "mysql",
		Status:  StatusStopped,
		LogFile: filepath.Join(config.LogDir, "mysql.log"),
	}

	return dsm
}

func (dsm *DockerServiceManager) dockerCompose(args ...string) *exec.Cmd {
	dockerPath := findDockerBinary()
	cmd := exec.Command(dockerPath, append([]string{"compose", "-f", dsm.composeFile}, args...)...)
	cmd.Dir = filepath.Dir(dsm.composeFile)
	return cmd
}

func (dsm *DockerServiceManager) dockerComposeWithTimeout(timeout time.Duration, args ...string) *exec.Cmd {
	dockerPath := findDockerBinary()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	_ = cancel
	cmd := exec.CommandContext(ctx, dockerPath, append([]string{"compose", "-f", dsm.composeFile}, args...)...)
	cmd.Dir = filepath.Dir(dsm.composeFile)
	return cmd
}

func (dsm *DockerServiceManager) StartNginx() error {
	svc := dsm.Services["nginx"]
	if svc.Status == StatusRunning {
		return fmt.Errorf("apache already running")
	}

	// Ensure docker nginx has the latest configs (and fastcgi_pass rewritten)
	if err := dsm.ReloadNginx(); err != nil {
		svc.Status = StatusError
		return fmt.Errorf("failed to prepare nginx config: %v", err)
	}

	// Start apache container
	cmd := dsm.dockerCompose("up", "-d", "apache")
	output, err := cmd.CombinedOutput()
	if err != nil {
		svc.Status = StatusError
		return fmt.Errorf("failed to start apache: %v\n%s", err, output)
	}

	svc.Status = StatusRunning
	svc.PID = 1 // Docker manages PID

	time.Sleep(1 * time.Second)
	return dsm.CheckNginxStatus()
}

func (dsm *DockerServiceManager) StopNginx() error {
	svc := dsm.Services["nginx"]
	if svc.Status == StatusStopped {
		return nil
	}

	cmd := dsm.dockerCompose("stop", "apache")
	cmd.Run()

	svc.Status = StatusStopped
	svc.PID = 0
	return nil
}

func (dsm *DockerServiceManager) ReloadNginx() error {
	// Regenerate apache vhosts into app support
	gen := apache.NewGenerator(dsm.Config)
	gen.GenerateAllVhosts()

	// Copy vhosts to repo ./apache/sites (mounted into container)
	srcSitesDir := filepath.Join(config.ConfigDir, "apache", "sites")
	dstSitesDir := filepath.Join(filepath.Dir(dsm.composeFile), "apache", "sites")
	os.MkdirAll(dstSitesDir, 0755)

	entries, err := os.ReadDir(srcSitesDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			in, rerr := os.ReadFile(filepath.Join(srcSitesDir, e.Name()))
			if rerr != nil {
				continue
			}
			_ = os.WriteFile(filepath.Join(dstSitesDir, e.Name()), in, 0644)
		}
	}

	// Reload apache in container if it's already running (ignore errors otherwise)
	cmd := dsm.dockerCompose("exec", "apache", "apachectl", "-k", "graceful")
	_ = cmd.Run()
	return nil
}

func (dsm *DockerServiceManager) CheckNginxStatus() error {
	cmd := dsm.dockerComposeWithTimeout(2*time.Second, "ps", "apache", "--format", "{{.Status}}")
	output, err := cmd.Output()
	if err != nil {
		dsm.Services["nginx"].Status = StatusStopped
		return fmt.Errorf("apache not running")
	}

	status := strings.TrimSpace(string(output))
	if strings.Contains(status, "Up") {
		dsm.Services["nginx"].Status = StatusRunning
		return nil
	}

	dsm.Services["nginx"].Status = StatusStopped
	return fmt.Errorf("apache not running")
}

func (dsm *DockerServiceManager) StartPHP() error {
	// Apache image already bundles PHP, nothing to start separately
	svc := dsm.Services["php-fpm"]
	svc.Status = StatusRunning
	svc.PID = 1
	return nil
}

func (dsm *DockerServiceManager) StopPHP() error {
	svc := dsm.Services["php-fpm"]
	svc.Status = StatusStopped
	svc.PID = 0
	return nil
}

func (dsm *DockerServiceManager) ReloadPHP() error {
	return nil
}

func (dsm *DockerServiceManager) RestartPHP() error {
	if err := dsm.StopPHP(); err != nil {
		return err
	}
	return dsm.StartPHP()
}

func (dsm *DockerServiceManager) CheckPHPStatus() error {
	// PHP status is tied to apache container
	if err := dsm.CheckNginxStatus(); err != nil {
		dsm.Services["php-fpm"].Status = StatusStopped
		return fmt.Errorf("php not running")
	}
	dsm.Services["php-fpm"].Status = StatusRunning
	return nil
}

func (dsm *DockerServiceManager) StartMySQL() error {
	svc := dsm.Services["mysql"]
	if svc.Status == StatusRunning {
		return fmt.Errorf("mysql already running")
	}

	cmd := dsm.dockerCompose("up", "-d", "mysql")
	output, err := cmd.CombinedOutput()
	if err != nil {
		svc.Status = StatusError
		return fmt.Errorf("failed to start mysql: %v\n%s", err, output)
	}

	svc.Status = StatusRunning
	svc.PID = 1

	time.Sleep(3 * time.Second)
	return dsm.CheckMySQLStatus()
}

func (dsm *DockerServiceManager) StopMySQL() error {
	svc := dsm.Services["mysql"]
	if svc.Status == StatusStopped {
		return nil
	}

	cmd := dsm.dockerCompose("stop", "mysql")
	cmd.Run()

	svc.Status = StatusStopped
	svc.PID = 0
	return nil
}

func (dsm *DockerServiceManager) CheckMySQLStatus() error {
	cmd := dsm.dockerComposeWithTimeout(2*time.Second, "ps", "mysql", "--format", "{{.Status}}")
	output, err := cmd.Output()
	if err != nil {
		dsm.Services["mysql"].Status = StatusStopped
		return fmt.Errorf("mysql not running")
	}

	status := strings.TrimSpace(string(output))
	if strings.Contains(status, "Up") {
		dsm.Services["mysql"].Status = StatusRunning
		return nil
	}

	dsm.Services["mysql"].Status = StatusStopped
	return fmt.Errorf("mysql not running")
}

func (dsm *DockerServiceManager) CreateDatabase(dbName, dbUser, dbPassword string) error {
	quotedDB := "`" + strings.ReplaceAll(dbName, "`", "``") + "`"
	// Escape SQL string values
	escapeSQL := func(s string) string {
		s = strings.ReplaceAll(s, "\\", "\\\\")
		s = strings.ReplaceAll(s, "'", "\\'")
		return s
	}
	userEsc := escapeSQL(dbUser)
	passEsc := escapeSQL(dbPassword)

	cmd := dsm.dockerCompose("exec", "mysql", "mysql", "-uroot", "-proot",
		"-e", fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;", quotedDB))
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create database: %v - %s", err, output)
	}

	// Create user if missing
	cmd = dsm.dockerCompose("exec", "mysql", "mysql", "-uroot", "-proot",
		"-e", fmt.Sprintf("CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED WITH mysql_native_password BY '%s';", userEsc, passEsc))
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create user: %v - %s", err, output)
	}

	// Always reset password to match project config (important for phpMyAdmin login)
	cmd = dsm.dockerCompose("exec", "mysql", "mysql", "-uroot", "-proot",
		"-e", fmt.Sprintf("ALTER USER '%s'@'%%' IDENTIFIED WITH mysql_native_password BY '%s';", userEsc, passEsc))
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to set user password: %v - %s", err, output)
	}

	cmd = dsm.dockerCompose("exec", "mysql", "mysql", "-uroot", "-proot",
		"-e", fmt.Sprintf("GRANT ALL PRIVILEGES ON %s.* TO '%s'@'%%'; FLUSH PRIVILEGES;", quotedDB, userEsc))
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to grant privileges: %v - %s", err, output)
	}

	return nil
}

func (dsm *DockerServiceManager) StartAll() error {
	// Start all services with docker-compose up -d
	cmd := dsm.dockerComposeWithTimeout(30*time.Second, "up", "-d")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to start services: %v\n%s", err, output)
	}

	dsm.Services["nginx"].Status = StatusRunning
	dsm.Services["php-fpm"].Status = StatusRunning
	dsm.Services["mysql"].Status = StatusRunning

	time.Sleep(3 * time.Second)
	dsm.RefreshStatuses()
	return nil
}

func (dsm *DockerServiceManager) StopAll() error {
	cmd := dsm.dockerCompose("down")
	cmd.Run()

	for _, svc := range dsm.Services {
		svc.Status = StatusStopped
		svc.PID = 0
	}
	return nil
}

func (dsm *DockerServiceManager) ReloadAll() error {
	return dsm.ReloadNginx()
}

func (dsm *DockerServiceManager) RefreshStatuses() {
	dsm.CheckNginxStatus()
	dsm.CheckPHPStatus()
	dsm.CheckMySQLStatus()
}

func (dsm *DockerServiceManager) GetServices() map[string]*Service {
	return dsm.Services
}

// CheckDockerAvailable verifies Docker is installed and running
func CheckDockerAvailable() bool {
	dockerPath := findDockerBinary()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, dockerPath, "version")
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

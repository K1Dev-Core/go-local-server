package brew

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

type Dependency string

const (
	Nginx Dependency = "nginx"
	PHP    Dependency = "php"
	MySQL  Dependency = "mysql"
)

type ProgressCallback func(percent int, message string)

type Manager struct{}

func NewManager() *Manager {
	return &Manager{}
}

func (m *Manager) IsInstalled(dep Dependency) bool {
	cmd := exec.Command("brew", "list", string(dep))
	err := cmd.Run()
	return err == nil
}

func (m *Manager) InstallWithProgress(dep Dependency, callback ProgressCallback) error {
	if m.IsInstalled(dep) {
		callback(100, "Already installed")
		return nil
	}
	
	callback(0, "Starting installation...")
	
	cmd := exec.Command("brew", "install", string(dep))
	
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	
	if err := cmd.Start(); err != nil {
		return err
	}
	
	progressRegex := regexp.MustCompile(`(\d+)%`)
	
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if matches := progressRegex.FindStringSubmatch(line); len(matches) > 1 {
				var percent int
				fmt.Sscanf(matches[1], "%d", &percent)
				callback(percent, line)
			} else {
				callback(-1, line)
			}
		}
	}()
	
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			callback(-1, line)
		}
	}()
	
	return cmd.Wait()
}

func (m *Manager) Install(dep Dependency) error {
	if m.IsInstalled(dep) {
		return nil
	}
	
	fmt.Printf("Installing %s via Homebrew...\n", dep)
	cmd := exec.Command("brew", "install", string(dep))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (m *Manager) GetBinPath(dep Dependency) string {
	cmd := exec.Command("brew", "--prefix", string(dep))
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	
	prefix := strings.TrimSpace(string(output))
	
	switch dep {
	case Nginx:
		return prefix + "/bin/nginx"
	case PHP:
		return prefix + "/sbin/php-fpm"
	case MySQL:
		return prefix + "/bin/mysqld"
	}
	
	return ""
}

func (m *Manager) DetectPath(dep Dependency) string {
	if path := m.GetBinPath(dep); path != "" {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	
	paths := []string{}
	switch dep {
	case Nginx:
		paths = []string{
			"/opt/homebrew/bin/nginx",
			"/usr/local/bin/nginx",
			"/opt/homebrew/opt/nginx/bin/nginx",
			"/usr/local/opt/nginx/bin/nginx",
		}
	case PHP:
		paths = []string{
			"/opt/homebrew/bin/php-fpm",
			"/usr/local/bin/php-fpm",
			"/opt/homebrew/opt/php/sbin/php-fpm",
			"/usr/local/opt/php/sbin/php-fpm",
		}
	case MySQL:
		paths = []string{
			"/opt/homebrew/bin/mysqld",
			"/usr/local/bin/mysqld",
			"/opt/homebrew/opt/mysql/bin/mysqld",
			"/usr/local/opt/mysql/bin/mysqld",
		}
	}
	
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	
	var binName string
	switch dep {
	case Nginx:
		binName = "nginx"
	case PHP:
		binName = "php-fpm"
	case MySQL:
		binName = "mysqld"
	}
	
	if binName != "" {
		cmd := exec.Command("which", binName)
		output, err := cmd.Output()
		if err == nil {
			path := strings.TrimSpace(string(output))
			if path != "" {
				return path
			}
		}
	}
	
	return ""
}

func (m *Manager) EnsureAll() error {
	deps := []Dependency{Nginx, PHP, MySQL}
	
	for _, dep := range deps {
		if !m.IsInstalled(dep) {
			if err := m.Install(dep); err != nil {
				return fmt.Errorf("failed to install %s: %w", dep, err)
			}
		}
	}
	
	return nil
}

func (m *Manager) GetVersion(dep Dependency) string {
	var cmd *exec.Cmd
	
	switch dep {
	case Nginx:
		cmd = exec.Command(m.GetBinPath(Nginx), "-v")
	case PHP:
		cmd = exec.Command(m.GetBinPath(PHP), "-v")
	case MySQL:
		cmd = exec.Command(m.GetBinPath(MySQL), "--version")
	default:
		return "unknown"
	}
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "not installed"
	}
	
	return strings.TrimSpace(string(output))
}

func (m *Manager) StartService(dep Dependency) error {
	cmd := exec.Command("brew", "services", "start", string(dep))
	return cmd.Run()
}

func (m *Manager) StopService(dep Dependency) error {
	cmd := exec.Command("brew", "services", "stop", string(dep))
	return cmd.Run()
}

func (m *Manager) RestartService(dep Dependency) error {
	cmd := exec.Command("brew", "services", "restart", string(dep))
	return cmd.Run()
}

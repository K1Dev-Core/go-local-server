package services

// ServiceManagerInterface defines the interface for service management.
// In Docker-only mode this is implemented by DockerServiceManager.
type ServiceManagerInterface interface {
	StartNginx() error
	StopNginx() error
	ReloadNginx() error
	CheckNginxStatus() error

	StartPHP() error
	StopPHP() error
	ReloadPHP() error
	CheckPHPStatus() error

	StartMySQL() error
	StopMySQL() error
	CheckMySQLStatus() error
	CreateDatabase(dbName, dbUser, dbPassword string) error

	StartAll() error
	StopAll() error
	ReloadAll() error
	RefreshStatuses()

	GetServices() map[string]*Service

	// Advanced features
	StreamContainerLogs(containerName string, follow bool) (chan string, error)
	GetAllHealthStatus() map[string]map[string]string
}

type ServiceStatus int

const (
	StatusStopped ServiceStatus = iota
	StatusRunning
	StatusError
)

type Service struct {
	Name       string
	Status     ServiceStatus
	PID        int
	StartCmd   string
	StopCmd    string
	ReloadCmd  string
	LogFile    string
	ConfigFile string
}

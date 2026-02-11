package main

import (
	"context"
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"fyne.io/systray"

	"go-local-server/internal/config"
	"go-local-server/internal/dns"
	"go-local-server/internal/livereload"
	"go-local-server/internal/projects"
	"go-local-server/internal/services"
	"go-local-server/pkg/apache"
)

func generateRandomHex(bytesLen int) string {
	b := make([]byte, bytesLen)
	if _, err := rand.Read(b); err != nil {
		// fallback: not cryptographically strong but avoids empty password
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func (a *App) refreshUIState() {
	// Apply latest service statuses immediately so the UI doesn't flash
	// incorrect button states when switching views.
	a.serviceManager.RefreshStatuses()
	a.updateServiceCards()
	a.updateQuickActionButtons()
	if a.busy {
		// Re-apply disabled/loading state to any buttons that were recreated
		// during a view switch.
		a.setBusy(true)
	}
}

func findDockerComposeFile() (string, error) {
	// Try current working directory
	if cwd, err := os.Getwd(); err == nil {
		candidate := filepath.Join(cwd, "docker-compose.yml")
		if st, err2 := os.Stat(candidate); err2 == nil && !st.IsDir() {
			return candidate, nil
		}
	}

	// Try executable location (../docker-compose.yml)
	exe, err := os.Executable()
	if err == nil {
		exeDir := filepath.Dir(exe)
		candidate := filepath.Clean(filepath.Join(exeDir, "..", "docker-compose.yml"))
		if st, err2 := os.Stat(candidate); err2 == nil && !st.IsDir() {
			return candidate, nil
		}
		
		// Try Mac app bundle Resources directory (GoLocalServer.app/Contents/Resources)
		resourcesDir := filepath.Clean(filepath.Join(exeDir, "..", "Resources"))
		candidate = filepath.Join(resourcesDir, "docker-compose.yml")
		if st, err2 := os.Stat(candidate); err2 == nil && !st.IsDir() {
			return candidate, nil
		}
		
		// Try one more level up (for nested app bundles)
		candidate = filepath.Clean(filepath.Join(exeDir, "..", "..", "Resources", "docker-compose.yml"))
		if st, err2 := os.Stat(candidate); err2 == nil && !st.IsDir() {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("docker-compose.yml not found")
}

func findDockerBinary() string {
	// Common Docker locations on macOS
	locations := []string{
		"/usr/local/bin/docker",
		"/opt/homebrew/bin/docker",
		"/usr/bin/docker",
		"/Applications/Docker.app/Contents/Resources/bin/docker",
		"/Applications/Docker.app/Contents/MacOS/docker",
		"/usr/local/docker/bin/docker",
	}
	
	// Add user's home directory paths
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
	
	// Fallback to "docker" and hope PATH works
	return "docker"
}

func (a *App) dockerCompose(args ...string) *exec.Cmd {
	dockerPath := findDockerBinary()
	cmd := exec.Command(dockerPath, append([]string{"compose", "-f", a.composeFile}, args...)...)
	cmd.Dir = filepath.Dir(a.composeFile)
	return cmd
}

func (a *App) runDockerComposeCommandWithLogs(title string, args ...string) {
	// Always run from the copied docker directory to avoid macOS Docker file-sharing issues
	// and to ensure all compose actions operate on the same project.
	dockerDir, err := a.copyDockerResources()
	if err != nil {
		a.showError("Docker Setup", fmt.Errorf("Failed to copy Docker resources: %v", err))
		return
	}

	logEntry := widget.NewMultiLineEntry()
	logEntry.Disable()

	progressBar := widget.NewProgressBarInfinite()

	statusLabel := widget.NewLabel(title + "...")
	statusLabel.TextStyle = fyne.TextStyle{Bold: true}

	closeBtn := widget.NewButton("Close", func() {})
	closeBtn.Disable()

	content := container.NewVBox(
		statusLabel,
		progressBar,
		widget.NewLabel("Log output:"),
		container.NewScroll(logEntry),
		closeBtn,
	)
	content.Resize(fyne.NewSize(700, 500))

	dlg := dialog.NewCustom("Docker Compose", "", content, a.mainWindow)
	dlg.Resize(fyne.NewSize(750, 550))

	closeBtn.OnTapped = func() {
		dlg.Hide()
	}

	dlg.Show()

	go func() {
		dockerPath := findDockerBinary()
		composeArgs := append([]string{"compose", "-f", "docker-compose.yml"}, args...)
		cmd := exec.Command(dockerPath, composeArgs...)
		cmd.Dir = dockerDir

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			statusLabel.SetText("Error: Failed to create stdout pipe")
			progressBar.Stop()
			closeBtn.Enable()
			return
		}

		stderr, err := cmd.StderrPipe()
		if err != nil {
			statusLabel.SetText("Error: Failed to create stderr pipe")
			progressBar.Stop()
			closeBtn.Enable()
			return
		}

		if err := cmd.Start(); err != nil {
			statusLabel.SetText(fmt.Sprintf("Error: %v", err))
			progressBar.Stop()
			closeBtn.Enable()
			return
		}

		scanner := bufio.NewScanner(stdout)
		stderrScanner := bufio.NewScanner(stderr)

		outputChan := make(chan string)
		doneChan := make(chan bool)

		go func() {
			for scanner.Scan() {
				outputChan <- scanner.Text()
			}
			doneChan <- true
		}()

		go func() {
			for stderrScanner.Scan() {
				outputChan <- "[ERROR] " + stderrScanner.Text()
			}
			doneChan <- true
		}()

		var fullLog strings.Builder
		activeReaders := 2
		for activeReaders > 0 {
			select {
			case line := <-outputChan:
				fullLog.WriteString(line + "\n")
				logEntry.SetText(fullLog.String())
				statusLabel.SetText(title + "...")
			case <-doneChan:
				activeReaders--
			}
		}

		err = cmd.Wait()
		progressBar.Stop()
		if err != nil {
			statusLabel.SetText(fmt.Sprintf("❌ Failed: %v", err))
			fullLog.WriteString(fmt.Sprintf("\n[EXIT ERROR] %v\n", err))
			logEntry.SetText(fullLog.String())
		} else {
			statusLabel.SetText("✅ Completed")
		}

		closeBtn.Enable()
		closeBtn.SetText("Close")
	}()
}

func (a *App) runDockerComposeWithLogs() {
	a.runDockerComposeCommandWithLogs("Docker Compose Up", "up", "-d", "--build")
}

// copyDockerResources copies docker files to user directory for Docker mounting
func (a *App) copyDockerResources() (string, error) {
	dockerDir := filepath.Join(config.ConfigDir, "docker")
	
	// Create directory
	if err := os.MkdirAll(dockerDir, 0755); err != nil {
		return "", err
	}
	
	// Find source resources
	exe, _ := os.Executable()
	exeDir := filepath.Dir(exe)
	var resourcesDir string
	
	// Try app bundle Resources
	candidate := filepath.Clean(filepath.Join(exeDir, "..", "Resources"))
	if _, err := os.Stat(filepath.Join(candidate, "docker-compose.yml")); err == nil {
		resourcesDir = candidate
	} else {
		// Fallback to current working directory
		if cwd, err := os.Getwd(); err == nil {
			if _, err := os.Stat(filepath.Join(cwd, "docker-compose.yml")); err == nil {
				resourcesDir = cwd
			}
		}
	}
	
	if resourcesDir == "" {
		return "", fmt.Errorf("docker-compose.yml not found")
	}
	
	// Copy docker-compose.yml
	srcCompose := filepath.Join(resourcesDir, "docker-compose.yml")
	dstCompose := filepath.Join(dockerDir, "docker-compose.yml")
	if err := copyFile(srcCompose, dstCompose); err != nil {
		return "", err
	}
	
	// Copy apache directory
	srcApache := filepath.Join(resourcesDir, "apache")
	dstApache := filepath.Join(dockerDir, "apache")
	if err := copyDir(srcApache, dstApache); err != nil {
		return "", err
	}

	// Copy php directory (php.ini)
	srcPHP := filepath.Join(resourcesDir, "php")
	dstPHP := filepath.Join(dockerDir, "php")
	if st, err := os.Stat(srcPHP); err == nil && st.IsDir() {
		if err := copyDir(srcPHP, dstPHP); err != nil {
			return "", err
		}
	}
	
	return dockerDir, nil
}

// copyFile copies a single file
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

// copyDir recursively copies a directory
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		
		dstPath := filepath.Join(dst, rel)
		
		if info.IsDir() {
			return os.MkdirAll(dstPath, info.Mode())
		}
		
		return copyFile(path, dstPath)
	})
}

func (a *App) reloadProjects() {
	gen := apache.NewGenerator(a.config)
	gen.GenerateAllVhosts()
	a.serviceManager.ReloadNginx()
	a.refreshProjectCards()
	a.updateStatus("Projects reloaded")
}

type App struct {
	fyneApp        fyne.App
	mainWindow     fyne.Window
	serviceManager services.ServiceManagerInterface
	projectManager *projects.Manager
	liveReload     *livereload.Manager
	dnsServer      *dns.Server
	config         *config.AppConfig
	usingDocker    bool
	composeFile    string
	stopRefreshCh  chan struct{}
	busy           bool
	busyPrevText   map[*widget.Button]string
	quickStartBtn  *widget.Button
	quickStopBtn   *widget.Button
	dockerUpBtn    *widget.Button
	restartBtn     *widget.Button

	sidebar          *fyne.Container
	contentArea      *fyne.Container
	currentView      string
	statusLabel      *widget.Label

	nginxCard  *serviceCard
	phpCard    *serviceCard
	mysqlCard  *serviceCard

	projectsContainer *fyne.Container
}

type serviceCard struct {
	name       string
	desc       string
	statusText *canvas.Text
	startBtn   *widget.Button
	stopBtn    *widget.Button
	indicator  *canvas.Circle
	container  *fyne.Container
}

func main() {
	fmt.Println("[DEBUG] Starting Go Local Server...")
	config.EnsureDirs()
	fmt.Println("[DEBUG] Config directories ensured")

	cfg := config.DefaultConfig()
	if err := cfg.Load(); err != nil {
		fmt.Printf("[DEBUG] Config load error: %v\n", err)
		cfg.Save()
	}
	fmt.Println("[DEBUG] Config loaded")

	a := &App{
		fyneApp:        app.NewWithID("com.golocalserver.app"),
		config:         cfg,
		serviceManager: nil, // Will be set below
		projectManager: projects.NewManager(cfg),
		dnsServer:      dns.NewServer(cfg),
		stopRefreshCh:  make(chan struct{}),
	}

	composeFile, err := findDockerComposeFile()
	if err == nil {
		a.composeFile = composeFile
		fmt.Printf("[DEBUG] Using docker compose file: %s\n", composeFile)
	} else {
		fmt.Printf("[DEBUG] docker compose file not found: %v\n", err)
	}

	// Docker-only mode
	a.serviceManager = services.NewDockerServiceManager(cfg)
	a.usingDocker = true
	a.liveReload = livereload.NewManager(0)
	fmt.Println("[DEBUG] App struct created")

	// go a.setupTray()
	// Systray disabled temporarily due to macOS signal handling issues
	
	fmt.Println("[DEBUG] Setting up main window...")
	a.setupMainWindow()
	fmt.Println("[DEBUG] Main window setup complete")
	
	a.generateConfigs()
	// DNS server disabled temporarily to prevent crashes
	// a.startDNSServer()
	
	// Load existing projects
	a.loadProjectsOnStartup()
	a.startLiveReloadForEnabledProjects()
	a.showDashboard()
	fmt.Println("[DEBUG] Dashboard shown")

	fmt.Println("[DEBUG] Showing window and starting app...")
	a.mainWindow.Show()
	
	// Always show Docker helper if Docker is not available
	if !services.CheckDockerAvailable() {
		go a.showDockerWarning()
	}
	
	a.fyneApp.Run()
}

func (a *App) showDockerWarning() {
	// Wait a moment for window to be fully shown
	time.Sleep(500 * time.Millisecond)

	dockerInstalled := findDockerBinary() != "docker"
	dockerRunning := services.CheckDockerAvailable()
	composeOK := a.composeFile != ""
	if composeOK {
		if st, err := os.Stat(a.composeFile); err != nil || st.IsDir() {
			composeOK = false
		}
	}

	info := widget.NewLabel(
		"Docker is not running or not installed.\n\n" +
			"If you want the app to manage services via Docker (recommended), start Docker Desktop and then run Docker Compose.\n\n" +
			"Current mode: Local binaries",
	)

	statusLine := func(label string, ok bool) fyne.CanvasObject {
		col := color.NRGBA{200, 80, 80, 255}
		val := "NO"
		if ok {
			col = color.NRGBA{80, 200, 120, 255}
			val = "YES"
		}
		l := canvas.NewText(fmt.Sprintf("%s: %s", label, val), col)
		l.TextSize = 12
		return l
	}

	statuses := container.NewVBox(
		statusLine("Docker Installed", dockerInstalled),
		statusLine("Docker Running", dockerRunning),
		statusLine("Compose File Found", composeOK),
	)

	openDockerBtn := widget.NewButtonWithIcon("Open Docker Desktop", theme.ComputerIcon(), func() {
		exec.Command("open", "-a", "Docker").Run()
	})

	downloadDockerBtn := widget.NewButtonWithIcon("Download Docker", theme.DownloadIcon(), func() {
		exec.Command("open", "https://www.docker.com/products/docker-desktop").Run()
	})

	runComposeBtn := widget.NewButtonWithIcon("Run Docker Compose", theme.MediaPlayIcon(), func() {
		if a.composeFile == "" {
			a.showError("Docker Compose", fmt.Errorf("docker-compose.yml not found"))
			return
		}
		a.runDockerComposeWithLogs()
	})

	resetStackBtn := widget.NewButtonWithIcon("Reset Stack (down -v)", theme.DeleteIcon(), func() {
		if a.composeFile == "" {
			a.showError("Docker Compose", fmt.Errorf("docker-compose.yml not found"))
			return
		}
		dialog.ShowConfirm("Reset Stack", "This will stop containers and remove volumes (MySQL data will be deleted). Continue?", func(ok bool) {
			if !ok {
				return
			}
			a.runDockerComposeCommandWithLogs("Docker Compose Down (volumes)", "down", "-v")
		}, a.mainWindow)
	})
	resetStackBtn.Importance = widget.DangerImportance

	refreshBtn := widget.NewButtonWithIcon("Re-check Docker", theme.ViewRefreshIcon(), func() {
		if services.CheckDockerAvailable() {
			a.serviceManager = services.NewDockerServiceManager(a.config)
			a.updateStatus("Docker detected - switched to Docker services")
			return
		}
		dialog.ShowInformation("Docker", "Docker is still not available. Please wait for Docker Desktop to fully start.", a.mainWindow)
	})

	content := container.NewVBox(
		info,
		widget.NewSeparator(),
		statuses,
		widget.NewSeparator(),
		openDockerBtn,
		downloadDockerBtn,
		runComposeBtn,
		resetStackBtn,
		refreshBtn,
	)
	content.Resize(fyne.NewSize(520, 260))

	dlg := dialog.NewCustom("Docker Setup", "Close", content, a.mainWindow)
	dlg.Resize(fyne.NewSize(560, 320))
	dlg.Show()
}

func (a *App) setupMainWindow() {
	a.mainWindow = a.fyneApp.NewWindow("Go Local Server")
	a.mainWindow.Resize(fyne.NewSize(1100, 750))
	a.mainWindow.SetCloseIntercept(func() {
		a.shutdown()
	})

	a.createSidebar()
	a.contentArea = container.NewMax()
	a.statusLabel = widget.NewLabel("Ready")

	creditsPrefix := canvas.NewText("Made by", color.NRGBA{120, 120, 120, 255})
	creditsPrefix.TextSize = 10
	creditsLink := widget.NewHyperlink("K1Dev", nil)
	creditsLink.OnTapped = func() {
		exec.Command("open", "https://github.com/K1Dev-Core").Run()
	}
	creditsRow := container.NewHBox(creditsPrefix, creditsLink)

	split := container.NewHSplit(a.sidebar, a.contentArea)
	split.Offset = 0.18

	footer := container.NewVBox(
		widget.NewSeparator(),
		container.NewBorder(nil, nil, nil, nil,
			container.NewHBox(
				a.statusLabel,
				layout.NewSpacer(),
				container.NewCenter(creditsRow),
				layout.NewSpacer(),
			),
		),
	)

	a.mainWindow.SetContent(container.NewBorder(
		nil, footer,
		nil, nil,
		split,
	))

	go a.refreshLoop()
}

func (a *App) shutdown() {
	select {
	case <-a.stopRefreshCh:
		// already closed
	default:
		close(a.stopRefreshCh)
	}
	if a.liveReload != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = a.liveReload.Stop(ctx)
		cancel()
	}

	// Best-effort cleanup
	if a.dnsServer != nil {
		a.dnsServer.Stop()
	}

	if a.fyneApp != nil {
		a.fyneApp.Quit()
	}
}

func (a *App) startLiveReloadForEnabledProjects() {
	if a.liveReload == nil {
		return
	}
	projectList, err := a.projectManager.List()
	if err != nil {
		return
	}
	for _, p := range projectList {
		if p != nil && p.LiveReloadEnabled {
			_ = a.liveReload.Enable(p)
			_ = a.liveReload.TryInjectScript(p)
		}
	}
}

func (a *App) withLoading(title string, fn func() error) {
	a.setBusy(true)
	
	// Create custom loading dialog with better UI
	loadingTitle := canvas.NewText(title, color.White)
	loadingTitle.TextSize = 16
	loadingTitle.TextStyle = fyne.TextStyle{Bold: true}
	
	loadingDesc := canvas.NewText("Please wait...", color.NRGBA{150, 150, 150, 255})
	loadingDesc.TextSize = 12
	
	// Animated dots using simple ASCII characters
	dots := canvas.NewText(".  ", color.NRGBA{100, 180, 255, 255})
	dots.TextSize = 20
	
	content := container.NewVBox(
		container.NewCenter(container.NewVBox(
			loadingTitle,
			widget.NewSeparator(),
			dots,
			loadingDesc,
		)),
	)
	content.Resize(fyne.NewSize(280, 100))
	
	loadingDlg := dialog.NewCustomWithoutButtons("", content, a.mainWindow)
	loadingDlg.Resize(fyne.NewSize(300, 120))
	
	// Animation for dots - use simple ASCII
	stopAnim := make(chan bool)
	go func() {
		frames := []string{".  ", ".. ", "...", " ..", "  .", " .."}
		i := 0
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				dots.Text = frames[i%len(frames)]
				dots.Refresh()
				i++
			case <-stopAnim:
				return
			}
		}
	}()
	
	loadingDlg.Show()

	go func() {
		err := fn()
		close(stopAnim)
		loadingDlg.Hide()
		if err != nil {
			a.showError(title, err)
		}
		a.setBusy(false)
	}()
}

func (a *App) setBusy(b bool) {
	if a.busyPrevText == nil {
		a.busyPrevText = make(map[*widget.Button]string)
	}

	if b {
		// Even if already busy, we still want to apply the disabled/loading state
		// to any newly created buttons after view changes.
		a.busy = true
		disableBtn := func(btn *widget.Button) {
			if btn == nil {
				return
			}
			if _, ok := a.busyPrevText[btn]; !ok {
				a.busyPrevText[btn] = btn.Text
			}
			btn.SetText("Loading...")
			btn.Disable()
		}
		disableBtn(a.quickStartBtn)
		disableBtn(a.quickStopBtn)
		disableBtn(a.dockerUpBtn)
		disableBtn(a.restartBtn)
		if a.nginxCard != nil {
			disableBtn(a.nginxCard.startBtn)
			disableBtn(a.nginxCard.stopBtn)
		}
		if a.phpCard != nil {
			disableBtn(a.phpCard.startBtn)
			disableBtn(a.phpCard.stopBtn)
		}
		if a.mysqlCard != nil {
			disableBtn(a.mysqlCard.startBtn)
			disableBtn(a.mysqlCard.stopBtn)
		}
		return
	}

	if !a.busy {
		return
	}
	a.busy = false
	enableBtn := func(btn *widget.Button) {
		if btn == nil {
			return
		}
		if prev, ok := a.busyPrevText[btn]; ok {
			btn.SetText(prev)
			delete(a.busyPrevText, btn)
		}
		btn.Enable()
	}
	enableBtn(a.quickStartBtn)
	enableBtn(a.quickStopBtn)
	enableBtn(a.dockerUpBtn)
	enableBtn(a.restartBtn)
	if a.nginxCard != nil {
		enableBtn(a.nginxCard.startBtn)
		enableBtn(a.nginxCard.stopBtn)
	}
	if a.phpCard != nil {
		enableBtn(a.phpCard.startBtn)
		enableBtn(a.phpCard.stopBtn)
	}
	if a.mysqlCard != nil {
		enableBtn(a.mysqlCard.startBtn)
		enableBtn(a.mysqlCard.stopBtn)
	}
}

func (a *App) createSidebar() {
	title := canvas.NewText("Go Local", color.White)
	title.TextSize = 22
	title.TextStyle = fyne.TextStyle{Bold: true}

	subtitle := canvas.NewText("Development Server", color.NRGBA{150, 150, 150, 255})
	subtitle.TextSize = 12

	navItems := []struct {
		label  string
		icon   fyne.Resource
		action func()
	}{
		{"Dashboard", theme.HomeIcon(), func() { a.showDashboard() }},
		{"Services", theme.SettingsIcon(), func() { a.showServices() }},
		{"Projects", theme.FolderIcon(), func() { a.showProjects() }},
		{"Settings", theme.DocumentCreateIcon(), func() { a.showSettings() }},
	}

	var navButtons []fyne.CanvasObject
	for _, item := range navItems {
		btn := widget.NewButtonWithIcon(item.label, item.icon, item.action)
		btn.Importance = widget.LowImportance
		btn.Alignment = widget.ButtonAlignLeading
		navButtons = append(navButtons, btn)
	}

	quickLabel := canvas.NewText("QUICK ACTIONS", color.NRGBA{100, 100, 100, 255})
	quickLabel.TextSize = 10

	startAllBtn := widget.NewButtonWithIcon("Start All", theme.MediaPlayIcon(), func() {
		a.withLoading("Starting services", func() error {
			if err := a.serviceManager.StartAll(); err != nil {
				return err
			}
			a.updateStatus("All services started")
			return nil
		})
	})
	startAllBtn.Importance = widget.SuccessImportance
	a.quickStartBtn = startAllBtn

	dockerUpBtn := widget.NewButtonWithIcon("Docker Up", theme.DownloadIcon(), func() {
		if a.composeFile == "" {
			a.showError("Docker Compose", fmt.Errorf("docker-compose.yml not found"))
			return
		}
		a.runDockerComposeWithLogs()
	})
	a.dockerUpBtn = dockerUpBtn

	restartContainersBtn := widget.NewButtonWithIcon("Restart Containers", theme.ViewRefreshIcon(), func() {
		if a.composeFile == "" {
			a.showError("Docker Compose", fmt.Errorf("docker-compose.yml not found"))
			return
		}

		// Use a clean reset flow to avoid "container name already in use" when compose
		// is run from different locations.
		a.runDockerComposeCommandWithLogs("Docker Compose Down", "down")
		a.runDockerComposeCommandWithLogs("Docker Compose Up", "up", "-d", "--build")
		a.updateStatus("Docker containers restarted")
	})
	a.restartBtn = restartContainersBtn

	stopAllBtn := widget.NewButtonWithIcon("Stop All", theme.MediaStopIcon(), func() {
		a.withLoading("Stopping services", func() error {
			if err := a.serviceManager.StopAll(); err != nil {
				return err
			}
			a.updateStatus("All services stopped")
			return nil
		})
	})
	stopAllBtn.Importance = widget.DangerImportance
	a.quickStopBtn = stopAllBtn

	portInfo := canvas.NewText(fmt.Sprintf("MySQL: %d | HTTP: %d", a.config.MySQLPort, a.config.HTTPPort), color.NRGBA{100, 100, 100, 255})
	portInfo.TextSize = 10

	// Add new buttons for Health Check and Logs
	toolsLabel := canvas.NewText("TOOLS", color.NRGBA{100, 100, 100, 255})
	toolsLabel.TextSize = 10

	healthBtn := widget.NewButtonWithIcon("Health Check", theme.InfoIcon(), func() {
		a.showHealthCheckDashboard()
	})

	logsBtn := widget.NewButtonWithIcon("Container Logs", theme.DocumentIcon(), func() {
		a.showContainerLogsDialog()
	})

	a.sidebar = container.NewVBox(
		container.NewPadded(container.NewVBox(title, subtitle)),
		widget.NewSeparator(),
		container.NewVBox(navButtons...),
		layout.NewSpacer(),
		quickLabel,
		startAllBtn,
		dockerUpBtn,
		restartContainersBtn,
		stopAllBtn,
		widget.NewSeparator(),
		toolsLabel,
		healthBtn,
		logsBtn,
		widget.NewSeparator(),
		portInfo,
	)
}

func (a *App) showDashboard() {
	a.currentView = "dashboard"

	welcomeTitle := canvas.NewText("Welcome to Go Local Server", color.White)
	welcomeTitle.TextSize = 28
	welcomeTitle.TextStyle = fyne.TextStyle{Bold: true}

	welcomeSub := canvas.NewText("Your local development environment is ready", color.NRGBA{150, 150, 150, 255})
	welcomeSub.TextSize = 14

	statusTitle := canvas.NewText("Service Status", color.White)
	statusTitle.TextSize = 18
	statusTitle.TextStyle = fyne.TextStyle{Bold: true}

	a.nginxCard = a.createServiceCard("Apache", "Apache Web Server", "Stopped")
	a.phpCard = a.createServiceCard("PHP-FPM", "PHP Processor", "Stopped")
	a.mysqlCard = a.createServiceCard("MySQL", fmt.Sprintf("Port %d", a.config.MySQLPort), "Stopped")

	cards := container.NewGridWithColumns(3,
		a.nginxCard.container,
		a.phpCard.container,
		a.mysqlCard.container,
	)

	projectsTitle := canvas.NewText("Recent Projects", color.White)
	projectsTitle.TextSize = 18
	projectsTitle.TextStyle = fyne.TextStyle{Bold: true}

	a.projectsContainer = container.NewVBox()
	a.refreshProjectCards()

	content := container.NewVBox(
		container.NewPadded(container.NewVBox(welcomeTitle, welcomeSub)),
		widget.NewSeparator(),
		container.NewPadded(statusTitle),
		cards,
		widget.NewSeparator(),
		container.NewPadded(projectsTitle),
		container.NewPadded(a.projectsContainer),
	)

	scroll := container.NewScroll(content)
	a.contentArea.Objects = []fyne.CanvasObject{scroll}
	a.contentArea.Refresh()
	a.refreshUIState()
}

func (a *App) showServices() {
	a.currentView = "services"

	title := canvas.NewText("Services", color.White)
	title.TextSize = 28
	title.TextStyle = fyne.TextStyle{Bold: true}

	desc := canvas.NewText("Manage your local development services", color.NRGBA{150, 150, 150, 255})
	desc.TextSize = 14

	a.nginxCard = a.createDetailedServiceCard("Apache", "Apache Web Server", "docker")
	a.phpCard = a.createDetailedServiceCard("PHP-FPM", "PHP Processor", "docker")
	a.mysqlCard = a.createDetailedServiceCard("MySQL", fmt.Sprintf("Database Server (Port %d)", a.config.MySQLPort), "docker")

	servicesList := container.NewVBox(
		a.nginxCard.container,
		widget.NewSeparator(),
		a.phpCard.container,
		widget.NewSeparator(),
		a.mysqlCard.container,
	)

	content := container.NewVBox(
		container.NewPadded(container.NewVBox(title, desc)),
		widget.NewSeparator(),
		servicesList,
	)

	scroll := container.NewScroll(content)
	a.contentArea.Objects = []fyne.CanvasObject{scroll}
	a.contentArea.Refresh()
	a.refreshUIState()
}

func (a *App) showProjects() {
	a.currentView = "projects"

	title := canvas.NewText("Projects", color.White)
	title.TextSize = 28
	title.TextStyle = fyne.TextStyle{Bold: true}

	desc := canvas.NewText(fmt.Sprintf("Your local projects on .%s domain", a.config.Domain), color.NRGBA{150, 150, 150, 255})
	desc.TextSize = 14

	toolbar := container.NewHBox(
		widget.NewButtonWithIcon("Add Project", theme.ContentAddIcon(), func() {
			a.showAddProjectDialog()
		}),
		widget.NewButtonWithIcon("Import Project", theme.DownloadIcon(), func() {
			dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
				if err != nil || uri == nil {
					return
				}
				selectedPath := uri.Path()
				a.showProjectDialog(nil, "", selectedPath, true)
			}, a.mainWindow)
		}),
		widget.NewButtonWithIcon("Reload All", theme.ViewRefreshIcon(), func() {
			a.withLoading("Reloading projects", func() error {
				a.reloadProjects()
				return nil
			})
		}),
	)

	a.projectsContainer = container.NewVBox()
	a.refreshProjectList()

	content := container.NewVBox(
		container.NewPadded(container.NewVBox(title, desc)),
		container.NewPadded(toolbar),
		widget.NewSeparator(),
		a.projectsContainer,
	)

	scroll := container.NewScroll(content)
	a.contentArea.Objects = []fyne.CanvasObject{scroll}
	a.contentArea.Refresh()
	a.refreshUIState()
}

func (a *App) showSettings() {
	a.currentView = "settings"

	title := canvas.NewText("Settings", color.White)
	title.TextSize = 28
	title.TextStyle = fyne.TextStyle{Bold: true}

	portTitle := canvas.NewText("Network Settings", color.White)
	portTitle.TextSize = 16
	portTitle.TextStyle = fyne.TextStyle{Bold: true}

	dbTitle := canvas.NewText("Database (Docker)", color.White)
	dbTitle.TextSize = 16
	dbTitle.TextStyle = fyne.TextStyle{Bold: true}

	dbInfo := canvas.NewText("Host: mysql  |  Port (inside Docker): 3306", color.NRGBA{120, 120, 120, 255})
	dbInfo.TextSize = 11

	httpPort := widget.NewEntry()
	httpPort.SetText(strconv.Itoa(a.config.HTTPPort))
	mysqlPort := widget.NewEntry()
	mysqlPort.SetText(strconv.Itoa(a.config.MySQLPort))
	domain := widget.NewEntry()
	domain.SetText(a.config.Domain)

	saveBtn := widget.NewButtonWithIcon("Save Settings", theme.DocumentSaveIcon(), func() {
		if p, err := strconv.Atoi(httpPort.Text); err == nil {
			a.config.HTTPPort = p
		}
		if p, err := strconv.Atoi(mysqlPort.Text); err == nil {
			a.config.MySQLPort = p
		}
		a.config.Domain = domain.Text
		a.config.Save()
		a.updateStatus("Settings saved")
	})
	saveBtn.Importance = widget.HighImportance

	content := container.NewVBox(
		container.NewPadded(title),
		widget.NewSeparator(),
		container.NewPadded(portTitle),
		container.NewPadded(widget.NewForm(
			widget.NewFormItem("HTTP Port", httpPort),
			widget.NewFormItem("MySQL Port", mysqlPort),
			widget.NewFormItem("Domain", domain),
		)),
		widget.NewSeparator(),
		container.NewPadded(dbTitle),
		container.NewPadded(dbInfo),
		widget.NewSeparator(),
		container.NewPadded(saveBtn),
	)

	scroll := container.NewScroll(content)
	a.contentArea.Objects = []fyne.CanvasObject{scroll}
	a.contentArea.Refresh()
	a.refreshUIState()
}

func (a *App) createServiceCard(name, desc, status string) *serviceCard {
	card := &serviceCard{name: name, desc: desc}

	card.indicator = canvas.NewCircle(color.NRGBA{100, 100, 100, 255})
	card.indicator.Resize(fyne.NewSize(12, 12))

	card.statusText = canvas.NewText(status, color.NRGBA{150, 150, 150, 255})
	card.statusText.TextSize = 12

	card.startBtn = widget.NewButtonWithIcon("Start", theme.MediaPlayIcon(), func() {
		a.startService(name)
	})
	card.startBtn.Importance = widget.SuccessImportance

	card.stopBtn = widget.NewButtonWithIcon("Stop", theme.MediaStopIcon(), func() {
		a.stopService(name)
	})
	card.stopBtn.Importance = widget.DangerImportance
	card.stopBtn.Hide()

	title := canvas.NewText(name, color.White)
	title.TextSize = 16
	title.TextStyle = fyne.TextStyle{Bold: true}

	description := canvas.NewText(desc, color.NRGBA{150, 150, 150, 255})
	description.TextSize = 12

	bg := canvas.NewRectangle(color.NRGBA{40, 40, 40, 255})
	bg.SetMinSize(fyne.NewSize(200, 100))

	card.container = container.NewStack(
		bg,
		container.NewPadded(container.NewBorder(
			nil, nil,
			container.NewVBox(card.indicator, title, description),
			nil,
			container.NewVBox(
				card.statusText,
				container.NewHBox(card.startBtn, card.stopBtn),
			),
		)),
	)

	return card
}

func (a *App) createDetailedServiceCard(name, desc, path string) *serviceCard {
	card := a.createServiceCard(name, desc, "Stopped")

	pathText := canvas.NewText(path, color.NRGBA{100, 100, 100, 255})
	pathText.TextSize = 10

	logBtn := widget.NewButtonWithIcon("View Logs", theme.DocumentIcon(), func() {
		a.openLog(filepath.Join(config.LogDir, fmt.Sprintf("%s.log", name)))
	})

	card.container = container.NewVBox(
		card.container,
		container.NewPadded(container.NewVBox(pathText, logBtn)),
	)

	return card
}

func (a *App) createProjectCard(p *projects.Project) fyne.CanvasObject {
	bg := canvas.NewRectangle(color.NRGBA{35, 35, 35, 255})
	bg.SetMinSize(fyne.NewSize(280, 120))

	title := canvas.NewText(p.Name, color.White)
	title.TextSize = 16
	title.TextStyle = fyne.TextStyle{Bold: true}

	url := fmt.Sprintf("http://%s", p.Domain)
	urlText := canvas.NewText(url, color.NRGBA{100, 150, 255, 255})
	urlText.TextSize = 12

	phpText := canvas.NewText(fmt.Sprintf("PHP %s", p.PHPVersion), color.NRGBA{150, 150, 150, 255})
	phpText.TextSize = 11

	dbText := canvas.NewText(fmt.Sprintf("DB: %s@%s", p.Database.DBUser, p.Database.DBHost), color.NRGBA{100, 100, 100, 255})
	dbText.TextSize = 10

	openBtn := widget.NewButtonWithIcon("Open", theme.ComputerIcon(), func() {
		exec.Command("open", url).Run()
	})
	openBtn.Importance = widget.HighImportance

	phpmyadminBtn := widget.NewButtonWithIcon("phpMyAdmin", theme.FolderOpenIcon(), func() {
		exec.Command("open", "http://localhost:8081").Run()
	})
	phpmyadminBtn.Importance = widget.SuccessImportance

	openFolderBtn := widget.NewButtonWithIcon("Folder", theme.FolderIcon(), func() {
		if p.Path != "" {
			exec.Command("open", p.Path).Run()
		}
	})

	openVSCodeBtn := widget.NewButtonWithIcon("VS Code", theme.ComputerIcon(), func() {
		if p.Path != "" {
			_ = exec.Command("open", "-a", "Visual Studio Code", p.Path).Run()
		}
	})

	copyURLBtn := widget.NewButtonWithIcon("Copy URL", theme.ContentCopyIcon(), func() {
		if a.mainWindow != nil {
			a.mainWindow.Clipboard().SetContent(url)
			a.updateStatus("Copied URL")
		}
	})

	copyDBBtn := widget.NewButtonWithIcon("Copy DB", theme.ContentCopyIcon(), func() {
		if p.Database.DBName == "" || p.Database.DBUser == "" {
			return
		}
		creds := fmt.Sprintf("DB Name: %s\nUser: %s\nPassword: %s\nHost: %s\nPort: %d",
			p.Database.DBName,
			p.Database.DBUser,
			p.Database.DBPassword,
			p.Database.DBHost,
			p.Database.DBPort,
		)
		if a.mainWindow != nil {
			a.mainWindow.Clipboard().SetContent(creds)
			a.updateStatus("Copied DB credentials")
		}
	})
	if p.Database.DBName == "" || p.Database.DBUser == "" {
		copyDBBtn.Hide()
	}

	editBtn := widget.NewButtonWithIcon("Edit", theme.DocumentCreateIcon(), func() {
		a.showEditProjectDialog(p)
	})

	deleteBtn := widget.NewButtonWithIcon("Delete", theme.DeleteIcon(), func() {
		a.deleteProject(p)
	})
	deleteBtn.Importance = widget.DangerImportance

	fixDBBtn := widget.NewButtonWithIcon("Fix DB", theme.ViewRefreshIcon(), func() {
		a.fixProjectDatabase(p)
	})
	fixDBBtn.Importance = widget.MediumImportance
	if p.Database.DBName == "" || p.Database.DBUser == "" {
		fixDBBtn.Hide()
	}

	liveReloadSwitch := widget.NewCheck("Live Reload", func(enabled bool) {
		a.setLiveReloadForProject(p, enabled)
	})
	liveReloadSwitch.SetChecked(p.LiveReloadEnabled)
	if p.Path == "" {
		liveReloadSwitch.Disable()
	}

	actionsBtn := widget.NewButtonWithIcon("Actions", theme.MenuIcon(), func() {
		content := container.NewGridWithColumns(2,
			phpmyadminBtn,
			openVSCodeBtn,
			copyURLBtn,
			copyDBBtn,
			fixDBBtn,
			deleteBtn,
		)
		d := dialog.NewCustom("Project Actions", "Close", container.NewPadded(content), a.mainWindow)
		d.Resize(fyne.NewSize(520, 220))
		d.Show()
	})

	return container.NewStack(
		bg,
		container.NewPadded(container.NewBorder(
			container.NewVBox(title, urlText, phpText, dbText),
			nil,
			nil,
			container.NewHBox(openBtn, openFolderBtn, liveReloadSwitch, editBtn, actionsBtn),
		)),
	)
}

func (a *App) toggleLiveReloadForProject(p *projects.Project) {
	if p == nil {
		return
	}
	a.setLiveReloadForProject(p, !p.LiveReloadEnabled)
}

func (a *App) setLiveReloadForProject(p *projects.Project, enabled bool) {
	if p == nil {
		return
	}
	if a.liveReload == nil {
		a.showError("Live Reload", fmt.Errorf("live reload is not available"))
		return
	}
	if p.LiveReloadEnabled == enabled {
		return
	}

	p.LiveReloadEnabled = enabled
	if err := a.projectManager.Update(p); err != nil {
		a.showError("Live Reload", err)
		return
	}

	if enabled {
		if err := a.liveReload.Enable(p); err != nil {
			a.showError("Live Reload", err)
			return
		}
		_ = a.liveReload.TryInjectScript(p)
		a.updateStatus(fmt.Sprintf("Live Reload enabled for '%s'", p.Name))
	} else {
		_ = a.liveReload.Disable(p.ID)
		a.updateStatus(fmt.Sprintf("Live Reload disabled for '%s'", p.Name))
	}

	a.refreshProjectCards()
	a.refreshProjectList()
}

func (a *App) fixProjectDatabase(p *projects.Project) {
	if p == nil {
		return
	}
	if p.Database.DBName == "" || p.Database.DBUser == "" {
		a.showError("Fix DB", fmt.Errorf("this project has no database configuration"))
		return
	}
	if a.serviceManager.GetServices()["mysql"].Status != services.StatusRunning {
		a.showError("Fix DB", fmt.Errorf("MySQL is not running"))
		return
	}

	a.withLoading("Fixing database", func() error {
		// Ensure password exists
		if strings.TrimSpace(p.Database.DBPassword) == "" {
			p.Database.DBPassword = generateRandomHex(8)
		}
		// Ensure docker host defaults
		p.Database.DBHost = "mysql"
		p.Database.DBPort = 3306

		if err := a.projectManager.Update(p); err != nil {
			return err
		}

		if err := a.serviceManager.CreateDatabase(p.Database.DBName, p.Database.DBUser, p.Database.DBPassword); err != nil {
			return err
		}

		creds := fmt.Sprintf("DB Name: %s\nUser: %s\nPassword: %s\nHost: %s\nPort: %d",
			p.Database.DBName,
			p.Database.DBUser,
			p.Database.DBPassword,
			p.Database.DBHost,
			p.Database.DBPort,
		)
		dialog.ShowInformation("Database Credentials", creds, a.mainWindow)
		a.updateStatus(fmt.Sprintf("DB fixed for '%s'", p.Name))
		return nil
	})
}

func (a *App) refreshProjectCards() {
	a.projectsContainer.Objects = nil

	projectList, _ := a.projectManager.List()
	if len(projectList) == 0 {
		emptyText := canvas.NewText("No projects yet. Click 'Add New Project' to create one.", color.NRGBA{100, 100, 100, 255})
		a.projectsContainer.Add(container.NewCenter(emptyText))
		return
	}

	for _, p := range projectList {
		a.projectsContainer.Add(a.createProjectCard(p))
	}
}

func (a *App) refreshProjectList() {
	a.projectsContainer.Objects = nil

	projectList, _ := a.projectManager.List()
	if len(projectList) == 0 {
		emptyText := canvas.NewText("No projects yet.", color.NRGBA{100, 100, 100, 255})
		a.projectsContainer.Add(container.NewCenter(emptyText))
		return
	}

	for _, p := range projectList {
		a.projectsContainer.Add(a.createProjectCard(p))
	}
}

func (a *App) startService(name string) {
	a.withLoading(fmt.Sprintf("Starting %s", name), func() error {
		var err error
		switch name {
		case "Nginx", "Apache":
			err = a.serviceManager.StartNginx()
		case "PHP-FPM":
			err = a.serviceManager.StartPHP()
		case "MySQL":
			err = a.serviceManager.StartMySQL()
		}
		if err != nil {
			a.updateStatus(fmt.Sprintf("%s failed: %v", name, err))
			return err
		}
		a.updateStatus(fmt.Sprintf("%s started", name))
		return nil
	})
}

func (a *App) stopService(name string) {
	a.withLoading(fmt.Sprintf("Stopping %s", name), func() error {
		var err error
		switch name {
		case "Nginx", "Apache":
			err = a.serviceManager.StopNginx()
		case "PHP-FPM":
			err = a.serviceManager.StopPHP()
		case "MySQL":
			err = a.serviceManager.StopMySQL()
		}
		if err != nil {
			a.updateStatus(fmt.Sprintf("%s stop failed: %v", name, err))
			return err
		}
		a.updateStatus(fmt.Sprintf("%s stopped", name))
		return nil
	})
}

func (a *App) refreshLoop() {
	// Use a slower ticker (3 seconds) to reduce CPU usage and make UI feel smoother
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			// Use goroutine to prevent blocking UI thread
			go func() {
				a.serviceManager.RefreshStatuses()
				a.updateServiceCards()
				a.updateQuickActionButtons()
			}()
		case <-a.stopRefreshCh:
			return
		}
	}
}

func (a *App) updateQuickActionButtons() {
	svcs := a.serviceManager.GetServices()
	ng := svcs["nginx"]
	php := svcs["php-fpm"]
	my := svcs["mysql"]

	allRunning := ng != nil && php != nil && my != nil &&
		ng.Status == services.StatusRunning &&
		php.Status == services.StatusRunning &&
		my.Status == services.StatusRunning

	allStopped := ng != nil && php != nil && my != nil &&
		ng.Status == services.StatusStopped &&
		php.Status == services.StatusStopped &&
		my.Status == services.StatusStopped

	if a.quickStartBtn != nil {
		if allRunning {
			a.quickStartBtn.Hide()
		} else {
			a.quickStartBtn.Show()
		}
	}
	if a.quickStopBtn != nil {
		if allStopped {
			a.quickStopBtn.Hide()
		} else {
			a.quickStopBtn.Show()
		}
	}
}

func (a *App) updateServiceCards() {
	nginxSvc := a.serviceManager.GetServices()["nginx"]
	phpSvc := a.serviceManager.GetServices()["php-fpm"]
	mysqlSvc := a.serviceManager.GetServices()["mysql"]

	if a.nginxCard != nil {
		if nginxSvc.Status == services.StatusRunning {
			a.nginxCard.statusText.Text = fmt.Sprintf("Running (PID:%d)", nginxSvc.PID)
			a.nginxCard.statusText.Color = color.NRGBA{100, 200, 100, 255}
			a.nginxCard.indicator.FillColor = color.NRGBA{100, 200, 100, 255}
			a.nginxCard.startBtn.Hide()
			a.nginxCard.stopBtn.Show()
		} else {
			a.nginxCard.statusText.Text = "Stopped"
			a.nginxCard.statusText.Color = color.NRGBA{150, 150, 150, 255}
			a.nginxCard.indicator.FillColor = color.NRGBA{100, 100, 100, 255}
			a.nginxCard.startBtn.Show()
			a.nginxCard.stopBtn.Hide()
		}
		a.nginxCard.indicator.Refresh()
		a.nginxCard.statusText.Refresh()
	}

	if a.phpCard != nil {
		if phpSvc.Status == services.StatusRunning {
			a.phpCard.statusText.Text = fmt.Sprintf("Running (PID:%d)", phpSvc.PID)
			a.phpCard.statusText.Color = color.NRGBA{100, 200, 100, 255}
			a.phpCard.indicator.FillColor = color.NRGBA{100, 200, 100, 255}
			a.phpCard.startBtn.Hide()
			a.phpCard.stopBtn.Show()
		} else {
			a.phpCard.statusText.Text = "Stopped"
			a.phpCard.statusText.Color = color.NRGBA{150, 150, 150, 255}
			a.phpCard.indicator.FillColor = color.NRGBA{100, 100, 100, 255}
			a.phpCard.startBtn.Show()
			a.phpCard.stopBtn.Hide()
		}
		a.phpCard.indicator.Refresh()
		a.phpCard.statusText.Refresh()
	}

	if a.mysqlCard != nil {
		if mysqlSvc.Status == services.StatusRunning {
			a.mysqlCard.statusText.Text = fmt.Sprintf("Running (PID:%d)", mysqlSvc.PID)
			a.mysqlCard.statusText.Color = color.NRGBA{100, 200, 100, 255}
			a.mysqlCard.indicator.FillColor = color.NRGBA{100, 200, 100, 255}
			a.mysqlCard.startBtn.Hide()
			a.mysqlCard.stopBtn.Show()
		} else {
			a.mysqlCard.statusText.Text = "Stopped"
			a.mysqlCard.statusText.Color = color.NRGBA{150, 150, 150, 255}
			a.mysqlCard.indicator.FillColor = color.NRGBA{100, 100, 100, 255}
			a.mysqlCard.startBtn.Show()
			a.mysqlCard.stopBtn.Hide()
		}
		a.mysqlCard.indicator.Refresh()
		a.mysqlCard.statusText.Refresh()
	}
}

func (a *App) showAddProjectDialog() {
	a.showProjectDialog(nil, "", "", false)
}

func (a *App) showEditProjectDialog(p *projects.Project) {
	a.showProjectDialog(p, "", "", false)
}

func (a *App) showProjectDialog(existing *projects.Project, prefillName, prefillPath string, isImport bool) {
	isEdit := existing != nil
	titleStr := "New Project"
	if isEdit {
		titleStr = "Edit Project"
	}
	if isImport {
		titleStr = "Import Project"
	}

	nameEntry := widget.NewEntry()
	nameEntry.SetPlaceHolder("Project Name")

	subdomainEntry := widget.NewEntry()
	subdomainEntry.SetPlaceHolder("myproject")

	pathEntry := widget.NewEntry()
	pathEntry.SetPlaceHolder("Select folder...")
	pathEntry.Disable()

	selectedPath := ""
	customSubdomain := ""
	if isEdit {
		nameEntry.SetText(existing.Name)
		selectedPath = existing.Path
		pathEntry.SetText(selectedPath)
		// Extract subdomain from domain (remove .test suffix)
		customSubdomain = existing.Domain
		if strings.HasSuffix(customSubdomain, "."+a.config.Domain) {
			customSubdomain = strings.TrimSuffix(customSubdomain, "."+a.config.Domain)
		}
		subdomainEntry.SetText(customSubdomain)
	} else if isImport {
		selectedPath = prefillPath
		pathEntry.SetText(selectedPath)
		if prefillName != "" {
			nameEntry.SetText(prefillName)
		} else if selectedPath != "" {
			nameEntry.SetText(filepath.Base(selectedPath))
		}
	}

	// Auto-generate subdomain from name if empty
	nameEntry.OnChanged = func(text string) {
		if subdomainEntry.Text == "" && text != "" {
			subdomainEntry.SetText(strings.ToLower(strings.ReplaceAll(text, " ", "-")))
		}
	}

	browseBtn := widget.NewButtonWithIcon("Browse", theme.FolderOpenIcon(), func() {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err != nil || uri == nil {
				return
			}
			selectedPath = uri.Path()
			pathEntry.SetText(selectedPath)
			if nameEntry.Text == "" {
				nameEntry.SetText(filepath.Base(selectedPath))
			}
		}, a.mainWindow)
	})

	pathRow := container.NewBorder(nil, nil, nil, browseBtn, pathEntry)

	phpVersion := widget.NewSelect([]string{"8.3", "8.2", "8.1", "8.0", "7.4"}, nil)
	phpVersion.SetSelected("8.3")

	// DocumentRoot input for custom subfolder
	docRootEntry := widget.NewEntry()
	docRootEntry.SetPlaceHolder("Leave empty for auto (public/ or root)")
	if isEdit && existing.DocumentRoot != "" {
		docRootEntry.SetText(existing.DocumentRoot)
	}

	// Template selection is only for new projects. For imports we auto-detect.
	templateType := widget.NewSelect([]string{"Simple", "MVC"}, nil)
	templateType.SetSelected("Simple")

	useMySQL := widget.NewCheck("Use MySQL", nil)
	useMySQL.SetChecked(true)

	dbName := widget.NewEntry()
	dbName.SetPlaceHolder("database_name")
	dbUser := widget.NewEntry()
	dbUser.SetPlaceHolder("username")
	dbPass := widget.NewPasswordEntry()
	dbPass.SetPlaceHolder("password")

	setDBFieldsEnabled := func(enabled bool) {
		if enabled {
			dbName.Enable()
			dbUser.Enable()
			dbPass.Enable()
		} else {
			dbName.Disable()
			dbUser.Disable()
			dbPass.Disable()
		}
	}
	setDBFieldsEnabled(true)
	useMySQL.OnChanged = func(b bool) {
		setDBFieldsEnabled(b)
	}

	genTemplate := widget.NewCheck("Generate PHP template", nil)
	// For imports we don't want to touch existing code by default
	genTemplate.SetChecked(!isEdit && !isImport)

	liveReloadCheck := widget.NewCheck("Enable Live Reload (auto refresh browser on save)", nil)
	if isEdit {
		liveReloadCheck.SetChecked(existing.LiveReloadEnabled)
	}

	if isEdit {
		phpVersion.SetSelected(existing.PHPVersion)
		if existing.Database.DBName != "" || existing.Database.DBUser != "" {
			useMySQL.SetChecked(true)
			setDBFieldsEnabled(true)
			dbName.SetText(existing.Database.DBName)
			dbUser.SetText(existing.Database.DBUser)
			dbPass.SetText(existing.Database.DBPassword)
		} else {
			useMySQL.SetChecked(false)
			setDBFieldsEnabled(false)
		}
	} else if isImport {
		// Auto-detect MVC layout on import
		if selectedPath != "" {
			if _, err := os.Stat(filepath.Join(selectedPath, "public", "index.php")); err == nil {
				templateType.SetSelected("MVC")
			}
		}
	}

	// Domain preview label
	domainPreview := canvas.NewText(fmt.Sprintf("-> %s.<domain>", a.config.Domain), color.NRGBA{120, 180, 255, 255})
	domainPreview.TextSize = 11

	// Helper labels
	docRootHint := canvas.NewText("Folder containing index.php (e.g: public, dist, www)", color.NRGBA{150, 150, 150, 255})
	docRootHint.TextSize = 10

	// Section 1: Basic Info Card
	basicSection := container.NewVBox(
		canvas.NewText("1. Basic Info", color.NRGBA{255, 255, 255, 255}),
		widget.NewForm(
			widget.NewFormItem("Name", nameEntry),
			widget.NewFormItem("Subdomain", container.NewVBox(subdomainEntry, domainPreview)),
			widget.NewFormItem("Path", pathRow),
		),
	)

	// Section 2: Web Server Card
	webSectionItems := []*widget.FormItem{
		widget.NewFormItem("PHP", phpVersion),
		widget.NewFormItem("DocumentRoot", container.NewVBox(docRootEntry, docRootHint)),
	}
	if !isImport {
		webSectionItems = append(webSectionItems, widget.NewFormItem("Template", templateType))
	}
	webSection := container.NewVBox(
		canvas.NewText("2. Web Server", color.NRGBA{255, 255, 255, 255}),
		widget.NewForm(webSectionItems...),
	)

	// Section 3: Database Card
	dbSection := container.NewVBox(
		canvas.NewText("3. Database (Optional)", color.NRGBA{255, 255, 255, 255}),
		useMySQL,
		widget.NewForm(
			widget.NewFormItem("DB Name", dbName),
			widget.NewFormItem("DB User", dbUser),
			widget.NewFormItem("Password", dbPass),
		),
	)

	// Template option
	templateSection := container.NewVBox()
	if !isImport {
		templateSection = container.NewVBox(
			widget.NewSeparator(),
			genTemplate,
		)
	}

	// Build main content with cards
	formContent := container.NewVBox(
		container.NewPadded(basicSection),
		widget.NewSeparator(),
		container.NewPadded(webSection),
		widget.NewSeparator(),
		container.NewPadded(dbSection),
		templateSection,
		widget.NewSeparator(),
		container.NewPadded(liveReloadCheck),
	)

	// Resize the form content for larger dialog
	formContent.Resize(fyne.NewSize(600, 500))

	// Create custom dialog with larger size
	dlg := dialog.NewCustom(titleStr, "Cancel", formContent, a.mainWindow)
	dlg.Resize(fyne.NewSize(700, 550))

	// Add save button with custom subdomain support
	saveBtn := widget.NewButton("Save", func() {
		if nameEntry.Text == "" || selectedPath == "" {
			a.showError("Validation Error", fmt.Errorf("name and path are required"))
			return
		}

		// Use custom subdomain or generate from name
		subdomain := subdomainEntry.Text
		if subdomain == "" {
			subdomain = strings.ToLower(strings.ReplaceAll(nameEntry.Text, " ", "-"))
		}

		// Clean subdomain - remove special characters
		subdomain = strings.ToLower(subdomain)
		subdomain = strings.ReplaceAll(subdomain, " ", "-")
		subdomain = strings.ReplaceAll(subdomain, "_", "-")

		domain := fmt.Sprintf("%s.%s", subdomain, a.config.Domain)

		finalDBName := strings.TrimSpace(dbName.Text)
		finalDBUser := strings.TrimSpace(dbUser.Text)
		finalDBPass := dbPass.Text

		// Keep existing values on edit if left blank
		if isEdit {
			if finalDBName == "" {
				finalDBName = existing.Database.DBName
			}
			if finalDBUser == "" {
				finalDBUser = existing.Database.DBUser
			}
			if finalDBPass == "" {
				finalDBPass = existing.Database.DBPassword
			}
		}

		var dbConfig projects.DatabaseConfig
		if useMySQL.Checked {
			// Auto-generate defaults if still empty
			if finalDBName == "" {
				finalDBName = subdomain
			}
			if finalDBUser == "" {
				finalDBUser = subdomain + "_user"
			}
			if finalDBPass == "" {
				finalDBPass = generateRandomHex(8)
			}

			dbConfig = projects.DatabaseConfig{
				DBName:     finalDBName,
				DBUser:     finalDBUser,
				DBPassword: finalDBPass,
				DBHost:     "mysql",
				DBPort:     3306,
			}
		} else {
			// No DB for this project
			dbConfig = projects.DatabaseConfig{DBHost: "", DBPort: 0}
		}

		var p *projects.Project
		var err error

		if isEdit {
			existing.Name = nameEntry.Text
			existing.Domain = domain
			existing.PHPVersion = phpVersion.Selected
			existing.Database = dbConfig
			existing.DocumentRoot = strings.TrimSpace(docRootEntry.Text)
			existing.LiveReloadEnabled = liveReloadCheck.Checked
			err = a.projectManager.Update(existing)
			p = existing
		} else {
			p, err = a.projectManager.CreateWithSubdomain(nameEntry.Text, subdomain, selectedPath, phpVersion.Selected, dbConfig)
			if p != nil {
				p.DocumentRoot = strings.TrimSpace(docRootEntry.Text)
				p.LiveReloadEnabled = liveReloadCheck.Checked
				a.projectManager.Update(p)
			}
		}

		if err != nil {
			a.showError("Error", err)
			return
		}

		if genTemplate.Checked && !isImport {
			if templateType.Selected == "MVC" {
				if err := a.projectManager.CopyMVCTemplate(p); err != nil {
					a.showError("MVC template error", err)
				}
			} else {
				a.projectManager.GeneratePHPIndex(p)
				a.projectManager.GeneratePHPInfo(p)
			}
		}
		a.projectManager.GenerateDBConfig(p)

		if useMySQL.Checked && a.serviceManager.GetServices()["mysql"].Status == services.StatusRunning {
			if err := a.serviceManager.CreateDatabase(dbConfig.DBName, dbConfig.DBUser, dbConfig.DBPassword); err != nil {
				a.showError("Database setup failed", err)
			} else {
				creds := fmt.Sprintf("DB Name: %s\nUser: %s\nPassword: %s\nHost: %s\nPort: %d",
					dbConfig.DBName,
					dbConfig.DBUser,
					dbConfig.DBPassword,
					dbConfig.DBHost,
					dbConfig.DBPort,
				)
				dialog.ShowInformation("Database Credentials", creds, a.mainWindow)
			}
		}

		gen := apache.NewGenerator(a.config)
		gen.GenerateVhost(p)
		a.serviceManager.ReloadNginx()

		if p.LiveReloadEnabled {
			_ = a.liveReload.Enable(p)
			_ = a.liveReload.TryInjectScript(p)
		} else {
			_ = a.liveReload.Disable(p.ID)
		}

		a.refreshProjectCards()
		a.updateStatus(fmt.Sprintf("Saved '%s' at %s", p.Name, p.Domain))
		dlg.Hide()
	})
	saveBtn.Importance = widget.HighImportance

	cancelBtn := widget.NewButton("Cancel", func() {
		dlg.Hide()
	})

	dlg.SetButtons([]fyne.CanvasObject{saveBtn, cancelBtn})
	dlg.Show()
}

func (a *App) deleteProject(p *projects.Project) {
	dialog.ShowConfirm("Delete Project", fmt.Sprintf("Delete '%s' at %s?", p.Name, p.Domain), func(ok bool) {
		if !ok {
			return
		}
		a.projectManager.Delete(p.ID)
		apache.NewGenerator(a.config).RemoveVhost(p.ID)
		a.serviceManager.ReloadNginx()
		a.refreshProjectCards()
		a.updateStatus(fmt.Sprintf("Deleted '%s'", p.Name))
	}, a.mainWindow)
}

func (a *App) generateConfigs() {
	gen := apache.NewGenerator(a.config)
	_ = gen.GenerateAllVhosts()
}

func (a *App) startDNSServer() {
	go func() {
		if err := a.dnsServer.Start(); err != nil {
			fmt.Printf("DNS error: %v\n", err)
		}
	}()
}

func (a *App) openLog(path string) {
	a.setBusy(true)
	progress := dialog.NewProgressInfinite("Loading log", "Please wait...", a.mainWindow)
	progress.Show()

	go func() {
		data, err := os.ReadFile(path)
		progress.Hide()
		a.setBusy(false)
		if err != nil {
			a.showError("Load log failed", err)
			return
		}

		const maxBytes = 200_000
		if len(data) > maxBytes {
			data = data[len(data)-maxBytes:]
		}

		entry := widget.NewMultiLineEntry()
		entry.SetText(string(data))
		entry.Disable()

		content := container.NewBorder(
			widget.NewLabel(filepath.Base(path)),
			nil,
			nil,
			nil,
			container.NewScroll(entry),
		)
		content.Resize(fyne.NewSize(900, 600))

		dlg := dialog.NewCustom("Log Viewer", "Close", content, a.mainWindow)
		dlg.Resize(fyne.NewSize(920, 640))
		dlg.Show()
	}()
}

func (a *App) showError(title string, err error) {
	dialog.ShowError(fmt.Errorf("%s: %w", title, err), a.mainWindow)
}

func (a *App) updateStatus(msg string) {
	a.statusLabel.SetText(msg)
}

func (a *App) setupTray() {
	systray.Run(func() {
		systray.SetIcon(getIcon())
		systray.SetTitle("GoLocal")
		systray.SetTooltip("Go Local Server")

		mShow := systray.AddMenuItem("Open App", "Open main window")
		mStartAll := systray.AddMenuItem("Start All", "Start services")
		mStopAll := systray.AddMenuItem("Stop All", "Stop services")
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("Quit", "Exit application")

		go func() {
			for {
				select {
				case <-mShow.ClickedCh:
					a.showMainWindow()
				case <-mStartAll.ClickedCh:
					a.serviceManager.StartAll()
					a.updateStatus("All services started")
				case <-mStopAll.ClickedCh:
					a.serviceManager.StopAll()
					a.updateStatus("All services stopped")
				case <-mQuit.ClickedCh:
					a.serviceManager.StopAll()
					a.dnsServer.Stop()
					systray.Quit()
					a.fyneApp.Quit()
					return
				}
			}
		}()
	}, nil)
}

func (a *App) showMainWindow() {
	a.mainWindow.Show()
	a.mainWindow.RequestFocus()
}

func (a *App) loadProjectsOnStartup() {
	projectList, err := a.projectManager.List()
	if err != nil {
		fmt.Printf("Error loading projects: %v\n", err)
		a.updateStatus("No projects found")
		return
	}
	
	if len(projectList) > 0 {
		a.updateStatus(fmt.Sprintf("Loaded %d project(s)", len(projectList)))
		// Also regenerate configs for existing projects
		gen := apache.NewGenerator(a.config)
		gen.GenerateAllVhosts()
	} else {
		a.updateStatus("No projects yet - create your first project!")
	}
}

func (a *App) showHealthCheckDashboard() {
	title := canvas.NewText("Health Check Dashboard", color.White)
	title.TextSize = 24
	title.TextStyle = fyne.TextStyle{Bold: true}

	// Get detailed health status
	healthStatus := a.serviceManager.GetAllHealthStatus()

	var rows []fyne.CanvasObject

	serviceNames := map[string]string{
		"apache":     "Apache Web Server",
		"mysql":      "MySQL Database",
		"phpmyadmin": "phpMyAdmin",
	}

	for serviceKey, info := range healthStatus {
		displayName := serviceNames[serviceKey]
		if displayName == "" {
			displayName = serviceKey
		}

		status := info["status"]
		health := info["health"]

		// Determine color based on status
		var indicatorColor color.NRGBA
		var statusText string

		switch status {
		case "running":
			indicatorColor = color.NRGBA{100, 200, 100, 255} // Green
			if health == "healthy" {
				statusText = "[OK] Ready"
			} else if health == "unknown" {
				statusText = "Running (no health check)"
			} else {
				statusText = fmt.Sprintf("Running (%s)", health)
			}
		case "stopped":
			indicatorColor = color.NRGBA{200, 100, 100, 255} // Red
			statusText = "[X] Stopped"
		default:
			indicatorColor = color.NRGBA{150, 150, 150, 255} // Gray
			statusText = "Unknown"
		}

		indicator := canvas.NewCircle(indicatorColor)
		indicator.Resize(fyne.NewSize(12, 12))

		nameText := canvas.NewText(displayName, color.White)
		nameText.TextSize = 14
		nameText.TextStyle = fyne.TextStyle{Bold: true}

		statusLabel := canvas.NewText(statusText, indicatorColor)
		statusLabel.TextSize = 12

		row := container.NewHBox(
			indicator,
			container.NewVBox(nameText, statusLabel),
		)
		rows = append(rows, row)
	}

	content := container.NewVBox(
		title,
		widget.NewSeparator(),
	)
	for _, row := range rows {
		content.Add(row)
	}

	// Auto-refresh button
	refreshBtn := widget.NewButton("Refresh", func() {
		a.showHealthCheckDashboard()
	})
	content.Add(refreshBtn)

	// Auto-start Docker button if Docker is not running
	if !services.CheckDockerAvailable() {
		dockerBtn := widget.NewButton("Start Docker Desktop", func() {
			if err := services.EnsureDockerRunning(); err != nil {
				a.showError("Docker Start", err)
			} else {
				dialog.ShowInformation("Docker", "Docker Desktop is starting... Please wait a moment and refresh.", a.mainWindow)
			}
		})
		dockerBtn.Importance = widget.HighImportance
		content.Add(widget.NewSeparator())
		content.Add(canvas.NewText("Docker is not running", color.NRGBA{255, 100, 100, 255}))
		content.Add(dockerBtn)
	}

	dlg := dialog.NewCustom("Health Check", "Close", content, a.mainWindow)
	dlg.Resize(fyne.NewSize(400, 300))
	dlg.Show()
}

func (a *App) showContainerLogsDialog() {
	title := canvas.NewText("Container Logs", color.White)
	title.TextSize = 24
	title.TextStyle = fyne.TextStyle{Bold: true}

	// Container selection
	containers := []string{"apache", "mysql", "phpmyadmin"}
	containerSelect := widget.NewSelect(containers, nil)
	containerSelect.SetSelected("apache")

	// Log display area
	logEntry := widget.NewMultiLineEntry()
	logEntry.SetPlaceHolder("Click 'View Logs' to see container output...")

	// Channel to stop log streaming
	var stopCh chan bool

	// View logs button
	viewBtn := widget.NewButton("View Logs", func() {
		// Clear previous logs
		logEntry.SetText("")

		// Stop previous stream if exists
		if stopCh != nil {
			close(stopCh)
		}
		stopCh = make(chan bool)

		selectedContainer := containerSelect.Selected

		// Start streaming logs
		go func() {
			logChan, err := a.serviceManager.StreamContainerLogs(selectedContainer, false)
			if err != nil {
				logEntry.SetText(fmt.Sprintf("Error: %v", err))
				return
			}

			var logs []string
			for line := range logChan {
				logs = append(logs, line)
				// Keep only last 100 lines
				if len(logs) > 100 {
					logs = logs[1:]
				}
				logEntry.SetText(strings.Join(logs, "\n"))
			}
		}()
	})

	// Follow logs checkbox
	followCheck := widget.NewCheck("Follow (real-time)", nil)

	// Auto-refresh logs when follow is checked
	go func() {
		for {
			time.Sleep(2 * time.Second)
			if followCheck.Checked && stopCh != nil {
				// Refresh logs in follow mode
				selectedContainer := containerSelect.Selected
				logChan, err := a.serviceManager.StreamContainerLogs(selectedContainer, true)
				if err == nil {
					var logs []string
					for line := range logChan {
						logs = append(logs, line)
						if len(logs) > 100 {
							logs = logs[1:]
						}
						logEntry.SetText(strings.Join(logs, "\n"))
					}
				}
			}
		}
	}()

	content := container.NewBorder(
		container.NewVBox(
			title,
			widget.NewSeparator(),
			container.NewHBox(widget.NewLabel("Container:"), containerSelect, viewBtn, followCheck),
		),
		nil, nil, nil,
		container.NewScroll(logEntry),
	)

	dlg := dialog.NewCustom("Container Logs", "Close", content, a.mainWindow)
	dlg.Resize(fyne.NewSize(700, 500))
	dlg.Show()
}

func getIcon() []byte {
	return []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x10, 0x00, 0x00, 0x00, 0x10, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0xF3, 0xFF, 0x61, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82}
}

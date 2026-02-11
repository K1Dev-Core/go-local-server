package livereload

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"go-local-server/internal/projects"
)

const DefaultPort = 35730

type projectState struct {
	watcher  *fsnotify.Watcher
	clients  map[string]chan struct{}
	debounce *time.Timer
}

type Manager struct {
	port int

	mu       sync.Mutex
	projects map[string]*projectState

	srv *http.Server
}

func NewManager(port int) *Manager {
	if port == 0 {
		port = DefaultPort
	}
	return &Manager{port: port, projects: make(map[string]*projectState)}
}

func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.srv != nil {
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/events", m.handleEvents)

	m.srv = &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", m.port), Handler: mux}
	go func() {
		_ = m.srv.ListenAndServe()
	}()
	return nil
}

func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	srv := m.srv
	m.srv = nil
	projects := m.projects
	m.projects = make(map[string]*projectState)
	m.mu.Unlock()

	for projectID := range projects {
		_ = m.Disable(projectID)
	}

	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

func (m *Manager) Enable(p *projects.Project) error {
	if p == nil {
		return fmt.Errorf("project is nil")
	}
	if err := m.Start(); err != nil {
		return err
	}

	m.mu.Lock()
	if _, ok := m.projects[p.ID]; ok {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	st := &projectState{watcher: watcher, clients: make(map[string]chan struct{})}

	// Start watching directories
	root := p.Path
	if root == "" {
		_ = watcher.Close()
		return fmt.Errorf("project path is empty")
	}

	if err := addWatchRecursive(watcher, root); err != nil {
		_ = watcher.Close()
		return err
	}

	m.mu.Lock()
	m.projects[p.ID] = st
	m.mu.Unlock()

	go m.watchLoop(p.ID, st)
	return nil
}

func (m *Manager) Disable(projectID string) error {
	m.mu.Lock()
	st := m.projects[projectID]
	delete(m.projects, projectID)
	m.mu.Unlock()

	if st == nil {
		return nil
	}

	for _, ch := range st.clients {
		close(ch)
	}

	if st.debounce != nil {
		st.debounce.Stop()
	}
	return st.watcher.Close()
}

func (m *Manager) EndpointURL(projectID string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/events?project=%s", m.port, projectID)
}

func (m *Manager) ClientScript(projectID string) string {
	// Use SSE (EventSource) for maximum compatibility
	return fmt.Sprintf(`<script>
(function(){
  try {
    var es = new EventSource('%s');
    es.onmessage = function(){ location.reload(); };
    es.onerror = function(){};
  } catch (e) {}
})();
</script>`, m.EndpointURL(projectID))
}

func (m *Manager) handleEvents(w http.ResponseWriter, r *http.Request) {
	// Allow EventSource from project domains (e.g. http://myproject.test)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	projectID := r.URL.Query().Get("project")
	if projectID == "" {
		http.Error(w, "missing project", http.StatusBadRequest)
		return
	}

	m.mu.Lock()
	st := m.projects[projectID]
	m.mu.Unlock()
	if st == nil {
		http.Error(w, "live reload not enabled for this project", http.StatusNotFound)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	clientID := randomID(8)
	ch := make(chan struct{}, 10)

	m.mu.Lock()
	// st may have been removed while we were building
	st2 := m.projects[projectID]
	if st2 == nil {
		m.mu.Unlock()
		http.Error(w, "live reload not enabled", http.StatusNotFound)
		return
	}
	st2.clients[clientID] = ch
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		if cur := m.projects[projectID]; cur != nil {
			delete(cur.clients, clientID)
		}
		m.mu.Unlock()
		close(ch)
	}()

	// Initial ping
	_, _ = fmt.Fprintf(w, "data: ready\n\n")
	flusher.Flush()

	notify := r.Context().Done()
	for {
		select {
		case <-notify:
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			_, _ = fmt.Fprintf(w, "data: reload\n\n")
			flusher.Flush()
		}
	}
}

func (m *Manager) watchLoop(projectID string, st *projectState) {
	trigger := func() {
		m.mu.Lock()
		cur := m.projects[projectID]
		m.mu.Unlock()
		if cur == nil {
			return
		}

		// Broadcast (non-blocking)
		for _, ch := range cur.clients {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}

	debounced := func() {
		if st.debounce != nil {
			st.debounce.Stop()
		}
		st.debounce = time.AfterFunc(250*time.Millisecond, trigger)
	}

	for {
		select {
		case ev, ok := <-st.watcher.Events:
			if !ok {
				return
			}
			// If a new directory was created, add watch
			if ev.Op&(fsnotify.Create) != 0 {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					_ = addWatchRecursive(st.watcher, ev.Name)
				}
			}

			if isIgnoredPath(ev.Name) {
				continue
			}
			// Only trigger on write/create/remove/rename
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
				debounced()
			}
		case _, ok := <-st.watcher.Errors:
			if !ok {
				return
			}
		}
	}
}

func addWatchRecursive(w *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if isIgnoredPath(path) {
				return filepath.SkipDir
			}
			_ = w.Add(path)
		}
		return nil
	})
}

func isIgnoredPath(path string) bool {
	p := strings.ToLower(path)
	ignored := []string{"/.git/", "/node_modules/", "/vendor/", "/.idea/", "/.vscode/", "/storage/", "/dist/", "/bin/"}
	for _, ig := range ignored {
		if strings.Contains(p, ig) {
			return true
		}
	}
	return false
}

func randomID(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// TryInjectScript attempts to inject the live reload client script into a project's entry file.
// It is best-effort and won't error if it cannot find a suitable file.
func (m *Manager) TryInjectScript(p *projects.Project) error {
	if p == nil {
		return nil
	}
	entryCandidates := []string{}

	// Prefer configured document root
	if strings.TrimSpace(p.DocumentRoot) != "" {
		entryCandidates = append(entryCandidates, filepath.Join(p.Path, p.DocumentRoot, "index.php"))
		entryCandidates = append(entryCandidates, filepath.Join(p.Path, p.DocumentRoot, "index.html"))
	}

	entryCandidates = append(entryCandidates,
		filepath.Join(p.Path, "public", "index.php"),
		filepath.Join(p.Path, "public", "index.html"),
		filepath.Join(p.Path, "index.php"),
		filepath.Join(p.Path, "index.html"),
	)

	// If we can't find a common entry file, best-effort scan for an index file.
	// Many existing projects use custom document roots (e.g. htdocs/, web/, src/).
	foundExistingCandidate := false
	for _, f := range entryCandidates {
		if _, err := os.Stat(f); err == nil {
			foundExistingCandidate = true
			break
		}
	}
	if !foundExistingCandidate {
		if found := findIndexFiles(p.Path, 3); len(found) > 0 {
			entryCandidates = append(entryCandidates, found...)
		}
	}

	script := m.ClientScript(p.ID)
	marker := "GoLocal LiveReload"
	for _, f := range entryCandidates {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), marker) {
			return nil
		}

		updated, ok := injectIntoHTML(f, string(data), script, marker)
		if !ok {
			continue
		}

		// Write back atomically
		tmp := f + ".tmp"
		if err := os.WriteFile(tmp, []byte(updated), 0644); err != nil {
			return err
		}
		return os.Rename(tmp, f)
	}
	return nil
}

func injectIntoHTML(filename string, src string, script string, marker string) (string, bool) {
	// Add marker comment to avoid duplicates
	injectBlock := fmt.Sprintf("\n<!-- %s -->\n%s\n", marker, script)

	lower := strings.ToLower(src)
	idx := strings.LastIndex(lower, "</body>")
	if idx >= 0 {
		return src[:idx] + injectBlock + src[idx:], true
	}

	// If it looks like an HTML/PHP page, append at end
	if strings.Contains(lower, "<html") || strings.Contains(lower, "<!doctype") || strings.Contains(lower, "<body") {
		return src + injectBlock, true
	}

	// PHP-safe fallback:
	// If this is a PHP entry file, it's common to not have </body> or even a closing ?>.
	// We need to ensure we're in HTML context before injecting the script, otherwise
	// we'll create nested PHP tags which causes parse errors.
	if strings.EqualFold(filepath.Ext(filename), ".php") {
		trimmed := strings.TrimSpace(src)
		
		// Check if file ends with an open PHP block (no closing ?>)
		// In this case we need to close PHP first, then inject script
		endsInPHPMode := false
		lastClose := strings.LastIndex(trimmed, "?>")
		lastOpen := strings.LastIndex(trimmed, "<?")
		
		if lastOpen > lastClose {
			// Last thing is <?php or <? - we're in PHP mode
			endsInPHPMode = true
		}
		
		if endsInPHPMode {
			// Close PHP context first, then inject script in HTML context
			phpMarker := fmt.Sprintf("\n\n/* %s */\n?>\n", marker)
			return src + phpMarker + script + "\n", true
		}
		
		// If not in PHP mode, or file ends properly with ?>, just append the script
		return src + "\n" + script + "\n", true
	}

	// Otherwise don't touch unknown files
	return src, false
}

func findIndexFiles(root string, maxDepth int) []string {
	var results []string
	rootClean := filepath.Clean(root)
	rootDepth := strings.Count(rootClean, string(os.PathSeparator))

	_ = filepath.WalkDir(rootClean, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if isIgnoredPath(path) {
				return filepath.SkipDir
			}
			depth := strings.Count(filepath.Clean(path), string(os.PathSeparator)) - rootDepth
			if maxDepth > 0 && depth > maxDepth {
				return filepath.SkipDir
			}
			return nil
		}

		name := strings.ToLower(d.Name())
		if name != "index.php" && name != "index.html" {
			return nil
		}
		if isIgnoredPath(path) {
			return nil
		}
		results = append(results, path)
		// Cap to avoid scanning too many candidates
		if len(results) >= 20 {
			return fsnotify.ErrEventOverflow
		}
		return nil
	})

	return results
}

// TailFile streams last N lines from a file (utility for future use).
func TailFile(path string, maxLines int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if maxLines > 0 && len(lines) > maxLines {
			lines = lines[len(lines)-maxLines:]
		}
	}
	return lines, scanner.Err()
}

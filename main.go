package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// version is set at build time via -ldflags.  Defaults to "dev" for local builds.
var version = "dev"

//go:embed templates/*
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

// ── API response types (mirrors daemon Status struct) ──────────────────

type daemonStatus struct {
	Version            string            `json:"version"`
	StartedAt          time.Time         `json:"started_at"`
	Uptime             string            `json:"uptime"`
	LastReconcile      *time.Time        `json:"last_reconcile"`
	LastReconcileError string            `json:"last_reconcile_error,omitempty"`
	ReconcileCount     int64             `json:"reconcile_count"`
	Containers         []containerStatus `json:"containers"`
	Watchers           []watcherStatus   `json:"watchers"`
	LKGHealthy         bool              `json:"lkg_healthy"`
	LKGError           string            `json:"lkg_error,omitempty"`
}

type containerStatus struct {
	Name      string     `json:"name"`
	Image     string     `json:"image"`
	Running   bool       `json:"running"`
	PID       uint32     `json:"pid,omitempty"`
	Healthy   *bool      `json:"healthy,omitempty"`
	Ports     []string   `json:"ports,omitempty"`
	Network   string     `json:"network,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
}

type watcherStatus struct {
	RepoURL  string     `json:"repo_url"`
	Branch   string     `json:"branch"`
	LastHash string     `json:"last_hash"`
	LastPoll *time.Time `json:"last_poll,omitempty"`
}

// ── CLI flags / env ─────────────────────────────────────────────────────

var (
	socketPath = envOrDefault("QM_SOCKET", "/run/quartermaster/daemon.sock")
	listenAddr = envOrDefault("QM_LISTEN", ":8090")
)

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	log.SetFlags(0)
	log.Printf("Quartermaster GUI v%s starting on %s", version, listenAddr)
	log.Printf("Daemon socket: %s", socketPath)

	funcs := template.FuncMap{
		"healthBadge":  healthBadge,
		"shortImage":   shortImage,
		"since":        since,
		"uptimeFrom":   uptimeFrom,
		"networkBadge": networkBadge,
		"portList":     portList,
		"add":          func(a, b int) int { return a + b },
	}

	// Parse all templates together so named fragments (e.g. "status-fragment")
	// can be included from "index.html".
	tmpl := template.Must(
		template.New("").Funcs(funcs).ParseFS(templatesFS, "templates/*.html"),
	)

	mux := http.NewServeMux()

	// Static files (CSS, HTMX, favicon)
	mux.Handle("/static/", http.FileServer(http.FS(staticFS)))

	// Main dashboard page
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		status, err := fetchStatus()
		pd := pageData{
			Status: status,
			Theme:  readTheme(r),
			Error: func() string {
				if err != nil {
					return err.Error()
				}
				return ""
			}(),
		}
		if status != nil {
			pd.Stats = computeStats(status)
		}
		renderOrError(w, tmpl.Lookup("index.html"), pd)
	})

	// HTML fragment: status table + watchers (polled by HTMX)
	mux.HandleFunc("/htmx/status", func(w http.ResponseWriter, r *http.Request) {
		status, err := fetchStatus()
		if err != nil {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<div class="banner error" hx-swap-oob="true" id="banner-area"><strong>⚠ Daemon unreachable</strong><p>` + err.Error() + `</p></div>`))
			return
		}

		// Apply search filter if query parameter present.
		q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		if q != "" {
			filtered := make([]containerStatus, 0, len(status.Containers))
			for _, c := range status.Containers {
				if strings.Contains(strings.ToLower(c.Name), q) ||
					strings.Contains(strings.ToLower(c.Image), q) {
					filtered = append(filtered, c)
				}
			}
			status.Containers = filtered
		}

		w.Header().Set("Content-Type", "text/html")
		tmpl.ExecuteTemplate(w, "status-fragment", struct {
			Status *daemonStatus
			Stats  statsData
		}{status, computeStats(status)})
	})

	// POST /htmx/reconcile — triggers immediate reconciliation
	mux.HandleFunc("/htmx/reconcile", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		resp, err := daemonClient().Post("http://unix/v1/reconcile", "application/json", nil)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
	})

	// POST /htmx/configmap — creates/updates a ConfigMap via daemon
	mux.HandleFunc("/htmx/configmap", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		if err := r.ParseForm(); err != nil {
			w.Write([]byte(`<span class="dim">Invalid form data</span>`))
			return
		}
		name := r.FormValue("name")
		key := r.FormValue("key")
		value := r.FormValue("value")
		if name == "" || key == "" || value == "" {
			w.Write([]byte(`<span class="dim">All fields required</span>`))
			return
		}

		body, _ := json.Marshal(map[string]interface{}{"data": map[string]string{key: value}})
		resp, err := daemonClient().Post("http://unix/v1/configmaps/"+name, "application/json", bytes.NewReader(body))
		if err != nil {
			w.Write([]byte(`<span class="dim" style="color:var(--red)">Error: ` + err.Error() + `</span>`))
			return
		}
		defer resp.Body.Close()

		// Trigger reconcile so the new ConfigMap is picked up.
		daemonClient().Post("http://unix/v1/reconcile", "application/json", nil)

		w.Write([]byte(`<span style="color:var(--green)">✓ ConfigMap "` + name + `" updated (` + key + `=` + value + `)</span>`))
	})

	// HTML fragment: restart a service via the daemon, then reload the panel
	mux.HandleFunc("/htmx/restart/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/htmx/restart/")
		if name == "" || strings.Contains(name, "/") {
			http.Error(w, "bad service name", 400)
			return
		}

		// Use a longer timeout — restart does stop + wait + kill + delete.
		rc := daemonClient()
		rc.Timeout = 30 * time.Second
		resp, err := rc.Post("http://unix/v1/services/"+name+"/restart", "application/json", nil)
		if err != nil {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<div class="banner error"><strong>⚠ Restart failed</strong><p>` + err.Error() + `</p></div>`))
			return
		}
		defer resp.Body.Close()

		// Reload the detail panel after a short delay (container takes a moment to come back).
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<div class="banner" style="border-color: ` + "var(--blue)" + `; color: ` + "var(--blue)" + `; background: rgba(88,166,255,0.08);"><strong>↻ Restarting ` + name + `...</strong><p>Container stopped. Redeploying — detail will refresh in 5s.</p></div>`))
		w.Write([]byte(`<div hx-get="/htmx/service/` + name + `" hx-trigger="load delay:5s" hx-target="#detail-panel" hx-swap="innerHTML"></div>`))
	})

	// HTML fragment: log viewer (polled by HTMX every 2s)
	mux.HandleFunc("/htmx/logs/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/htmx/logs/")
		if name == "" || strings.Contains(name, "/") {
			http.Error(w, "bad service name", 400)
			return
		}

		// Use the tail query parameter if provided.
		tail := r.URL.Query().Get("tail")
		if tail == "" {
			tail = "4096"
		}
		resp, err := daemonClient().Get("http://unix/v1/services/" + name + "/logs?tail=" + tail)
		if err != nil {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<pre class="log-content log-error">` + err.Error() + `</pre>`))
			return
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		logText := string(body)

		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<pre class="log-content">` + template.HTMLEscapeString(logText) + `</pre>`))
	})

	// HTML fragment: service detail panel (loaded on click)
	mux.HandleFunc("/htmx/service/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/htmx/service/")
		if name == "" || strings.Contains(name, "/") {
			http.Error(w, "bad service name", 400)
			return
		}

		svc, err := fetchService(name)
		if err != nil {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<div class="banner error"><strong>⚠ Error</strong><p>` + err.Error() + `</p></div>`))
			return
		}

		// Also fetch current status for health info.
		status, _ := fetchStatus()

		w.Header().Set("Content-Type", "text/html")
		tmpl.ExecuteTemplate(w, "service-detail", detailPageData{
			Service: svc,
			Status:  status,
		})
	})

	// POST /htmx/theme/toggle — flips between light and dark theme.
	mux.HandleFunc("/htmx/theme/toggle", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		current := readTheme(r)
		next := "dark"
		if current == "dark" {
			next = "light"
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "theme",
			Value:    next,
			Path:     "/",
			MaxAge:   365 * 24 * 3600,
			SameSite: http.SameSiteLaxMode,
			HttpOnly: true,
		})
		// Reload the page so the <html data-theme> attribute is re-rendered.
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
	})

	// Health endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("GUI ready → http://localhost%s", listenAddr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// ── Page data ────────────────────────────────────────────────────────────

type pageData struct {
	Status *daemonStatus
	Stats  statsData
	Theme  string
	Query  string
	Error  string
}

type detailPageData struct {
	Service *serviceDetail
	Status  *daemonStatus
}

type statsData struct {
	Running   int
	Total     int
	Unhealthy int
	Reconcile int64
}

func computeStats(s *daemonStatus) statsData {
	st := statsData{
		Total:     len(s.Containers),
		Reconcile: s.ReconcileCount,
	}
	for _, c := range s.Containers {
		if c.Running {
			st.Running++
		}
		if c.Healthy != nil && !*c.Healthy {
			st.Unhealthy++
		}
	}
	return st
}

// ── Daemon client (Unix socket HTTP) ─────────────────────────────────────

func daemonClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
		Timeout: 8 * time.Second,
	}
}

func fetchStatus() (*daemonStatus, error) {
	resp, err := daemonClient().Get("http://unix/v1/status")
	if err != nil {
		return nil, fmt.Errorf("cannot reach daemon: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("daemon returned HTTP %d", resp.StatusCode)
	}

	var s daemonStatus
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, fmt.Errorf("bad response: %w", err)
	}
	return &s, nil
}

// serviceDetail is the JSON shape returned by /v1/services/<name>.
type serviceDetail struct {
	Name          string           `json:"name"`
	Image         string           `json:"image"`
	RestartPolicy string           `json:"restart_policy"`
	Ports         []portDetail     `json:"ports,omitempty"`
	Volumes       []volumeDetail   `json:"volumes,omitempty"`
	Env           []envDetail      `json:"env,omitempty"`
	Secrets       []secretRef      `json:"secrets,omitempty"`
	Network       string           `json:"network,omitempty"`
	User          string           `json:"user,omitempty"`
	DependsOn     []string         `json:"depends_on,omitempty"`
	HealthCheck   *healthCheck     `json:"healthcheck,omitempty"`
	Resources     *resourcesDetail `json:"resources,omitempty"`
	Command       []string         `json:"command,omitempty"`
	Ingress       *ingressConfig   `json:"ingress,omitempty"`
}

type portDetail struct {
	Host      int    `json:"host"`
	Container int    `json:"container"`
	Protocol  string `json:"protocol,omitempty"`
}

type volumeDetail struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Type   string `json:"type"`
}

type envDetail struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type secretRef struct {
	Name      string `json:"name"`
	SecretRef string `json:"secret_ref"`
}

type healthCheck struct {
	Type     string `json:"type"`
	Path     string `json:"path,omitempty"`
	Port     int    `json:"port,omitempty"`
	Interval string `json:"interval"`
}

type resourcesDetail struct {
	GPU *gpuResource `json:"gpu,omitempty"`
}

type gpuResource struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type ingressConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	Auth bool   `json:"auth"`
}

func fetchService(name string) (*serviceDetail, error) {
	resp, err := daemonClient().Get("http://unix/v1/services/" + name)
	if err != nil {
		return nil, fmt.Errorf("cannot reach daemon: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("service %q not found", name)
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("daemon returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var s serviceDetail
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, fmt.Errorf("bad response: %w", err)
	}
	return &s, nil
}

// ── Template helpers ─────────────────────────────────────────────────────

func healthBadge(healthy *bool) template.HTML {
	if healthy == nil {
		return `<span class="badge muted">—</span>`
	}
	if *healthy {
		return `<span class="badge ok">✓ healthy</span>`
	}
	return `<span class="badge fail">✗ unhealthy</span>`
}

func shortImage(ref string) string {
	parts := strings.Split(ref, "/")
	last := parts[len(parts)-1]
	if len(last) > 40 {
		last = last[:37] + "..."
	}
	return last
}

func since(t *time.Time) string {
	if t == nil {
		return "never"
	}
	d := time.Since(*t).Truncate(time.Second)
	if d < time.Minute {
		return d.String() + " ago"
	}
	return d.Truncate(time.Second).String() + " ago"
}

// uptimeFrom returns a short human-readable duration since t (for container uptime).
func uptimeFrom(t *time.Time) string {
	if t == nil {
		return "—"
	}
	d := time.Since(*t)
	if d < time.Minute {
		return "<1m"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	days := int(d.Hours()) / 24
	return fmt.Sprintf("%dd%dh", days, int(d.Hours())%24)
}

// networkBadge returns a coloured pill for the network profile.
func networkBadge(network string) template.HTML {
	if network == "" {
		return ``
	}
	class := network
	switch network {
	case "public":
		class = "public"
	case "internal":
		class = "internal"
	case "vpn":
		class = "vpn"
	case "host":
		class = "host"
	default:
		return template.HTML(`<span class="network-badge">` + template.HTMLEscapeString(network) + `</span>`)
	}
	return template.HTML(`<span class="network-badge ` + class + `">` + template.HTMLEscapeString(network) + `</span>`)
}

// portList formats a slice of port strings as comma-separated HTML spans.
func portList(ports []string) template.HTML {
	if len(ports) == 0 {
		return `—`
	}
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = template.HTMLEscapeString(p)
	}
	return template.HTML(strings.Join(parts, ", "))
}

// readTheme returns the current theme from the cookie, defaulting to "dark".
func readTheme(r *http.Request) string {
	c, err := r.Cookie("theme")
	if err != nil || (c.Value != "light" && c.Value != "dark") {
		return "dark"
	}
	return c.Value
}

func renderOrError(w http.ResponseWriter, tmpl *template.Template, data interface{}) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		log.Printf("Template error: %v", err)
		http.Error(w, "Internal Server Error", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

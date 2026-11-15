// notes-api: malé REST API na poznámky, dáta drží v PostgreSQL.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	_ "github.com/lib/pq"
)

// version sa nastavuje pri builde: go build -ldflags "-X main.version=1.0.0"
var version = "dev"

const maxTextLen = 500

type config struct {
	Port       string
	Env        string
	DBHost     string
	DBPort     string
	DBName     string
	DBUser     string
	DBPassword string
	DBSSLMode  string
}

type app struct {
	db          *sql.DB
	cfg         config
	log         *slog.Logger
	hostname    string
	schemaReady atomic.Bool
	ready       atomic.Bool
}

type note struct {
	ID        int64     `json:"id"`
	Text      string    `json:"text"`
	Host      string    `json:"host"`
	CreatedAt time.Time `json:"created_at"`
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func loadConfig() config {
	return config{
		Port:       getenv("PORT", "8080"),
		Env:        getenv("APP_ENV", "local"),
		DBHost:     getenv("DB_HOST", "localhost"),
		DBPort:     getenv("DB_PORT", "5432"),
		DBName:     getenv("DB_NAME", "notes"),
		DBUser:     getenv("DB_USER", "notes"),
		DBPassword: os.Getenv("DB_PASSWORD"),
		DBSSLMode:  getenv("DB_SSLMODE", "disable"),
	}
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cfg := loadConfig()

	if cfg.DBPassword == "" {
		logger.Error("chýba premenná prostredia DB_PASSWORD, končím")
		os.Exit(1)
	}

	dsn := fmt.Sprintf("host=%s port=%s dbname=%s user=%s password=%s sslmode=%s connect_timeout=3",
		cfg.DBHost, cfg.DBPort, cfg.DBName, cfg.DBUser, cfg.DBPassword, cfg.DBSSLMode)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		logger.Error("neplatné nastavenie databázy", "error", err)
		os.Exit(1)
	}
	db.SetMaxOpenConns(10)
	db.SetConnMaxIdleTime(time.Minute)

	hostname, _ := os.Hostname()
	a := &app{db: db, cfg: cfg, log: logger, hostname: hostname}

	logger.Info("štartujem notes-api",
		"version", version, "env", cfg.Env, "port", cfg.Port,
		"db_host", cfg.DBHost, "db_port", cfg.DBPort, "db_name", cfg.DBName, "db_user", cfg.DBUser)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.handleHealthz)
	mux.HandleFunc("GET /readyz", a.handleReadyz)
	mux.HandleFunc("GET /api/info", a.handleInfo)
	mux.HandleFunc("GET /api/notes", a.handleListNotes)
	mux.HandleFunc("POST /api/notes", a.handleCreateNote)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           a.logRequests(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	// Pripojenie k databáze a vytvorenie tabuľky skúšame na pozadí,
	// aby HTTP server bežal (a /healthz odpovedal) aj keď databáza ešte nie je dostupná.
	go a.connectLoop(ctx)

	go func() {
		logger.Info("HTTP server počúva", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server spadol", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("dostal som signál na ukončenie, dokončujem rozbehnuté požiadavky")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("ukončenie servera zlyhalo", "error", err)
	}
	db.Close()
	logger.Info("koniec")
}

// connectLoop skúša databázu každých 5 sekúnd, kým nie je pripravená. Potom skončí
// a stav ďalej kontroluje /readyz.
func (a *app) connectLoop(ctx context.Context) {
	for {
		err := a.checkDB(ctx)
		a.markReady(err, true)
		if err == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// checkDB overí spojenie s databázou a pri prvom úspechu vytvorí tabuľku.
func (a *app) checkDB(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	if err := a.db.PingContext(ctx); err != nil {
		return err
	}
	if !a.schemaReady.Load() {
		if err := a.initSchema(ctx); err != nil {
			return fmt.Errorf("vytvorenie tabuľky zlyhalo: %w", err)
		}
		a.schemaReady.Store(true)
		a.log.Info("tabuľka notes je pripravená")
	}
	return nil
}

func (a *app) initSchema(ctx context.Context) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Zámok, aby viac replík naraz nevytváralo tú istú tabuľku.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(424242)`); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS notes (
			id         BIGSERIAL PRIMARY KEY,
			text       TEXT        NOT NULL,
			host       TEXT        NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// markReady zapíše stav. Do logu ide vždy zmena stavu, neúspešný pokus len keď logEveryFailure.
func (a *app) markReady(err error, logEveryFailure bool) {
	if err == nil {
		if !a.ready.Swap(true) {
			a.log.Info("databáza je dostupná, appka je pripravená")
		}
		return
	}
	if a.ready.Swap(false) {
		a.log.Warn("databáza prestala byť dostupná", "error", err)
	} else if logEveryFailure {
		a.log.Warn("databáza zatiaľ nie je dostupná, skúsim znova o 5 s", "error", err)
	}
}

func (a *app) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *app) handleReadyz(w http.ResponseWriter, r *http.Request) {
	err := a.checkDB(r.Context())
	a.markReady(err, false)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (a *app) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"app":      "notes-api",
		"version":  version,
		"env":      a.cfg.Env,
		"hostname": a.hostname,
	})
}

func (a *app) handleListNotes(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.QueryContext(r.Context(),
		`SELECT id, text, host, created_at FROM notes ORDER BY id DESC LIMIT 100`)
	if err != nil {
		a.dbError(w, err)
		return
	}
	defer rows.Close()

	notes := []note{}
	for rows.Next() {
		var n note
		if err := rows.Scan(&n.ID, &n.Text, &n.Host, &n.CreatedAt); err != nil {
			a.dbError(w, err)
			return
		}
		notes = append(notes, n)
	}
	if err := rows.Err(); err != nil {
		a.dbError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, notes)
}

func (a *app) handleCreateNote(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `očakávam JSON v tvare {"text": "..."}`})
		return
	}
	in.Text = strings.TrimSpace(in.Text)
	if in.Text == "" || len([]rune(in.Text)) > maxTextLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("text je povinný a môže mať najviac %d znakov", maxTextLen),
		})
		return
	}

	n := note{Text: in.Text, Host: a.hostname}
	err := a.db.QueryRowContext(r.Context(),
		`INSERT INTO notes (text, host) VALUES ($1, $2) RETURNING id, created_at`,
		n.Text, n.Host).Scan(&n.ID, &n.CreatedAt)
	if err != nil {
		a.dbError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

func (a *app) dbError(w http.ResponseWriter, err error) {
	a.log.Error("chyba databázy", "error", err)
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "databáza nie je dostupná"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// logRequests zapíše do logu každú požiadavku okrem /healthz a /readyz (tie volá Kubernetes každých pár sekúnd).
func (a *app) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		a.log.Info("request",
			"method", r.Method, "path", r.URL.Path, "status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(), "remote", r.RemoteAddr)
	})
}

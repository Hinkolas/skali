// deployment-timing measures a release migration and a small HTTP service
// using managed PostgreSQL and S3. Skali's edge terminates public HTTPS.
package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

//go:embed migrations/001_records.sql
var migration string

//go:embed version.txt
var version string

const marker = "_timing/readiness.txt"

var keyPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

type service struct {
	db            *sql.DB
	s3            *minio.Client
	bucket, token string
	started       time.Time
	readyLogged   atomic.Bool
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	started := time.Now()
	mode := "serve"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	slog.Info("process_started", "mode", mode, "version", strings.TrimSpace(version))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s, err := openService(started)
	if err != nil {
		slog.Error("configuration_failed")
		os.Exit(1)
	}
	defer s.db.Close()
	if mode == "migrate" {
		migrationCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		if err := s.migrate(migrationCtx); err != nil {
			slog.Error("migration_failed")
			os.Exit(1)
		}
		slog.Info("migration_completed", "elapsed_ms", time.Since(started).Milliseconds())
		return
	}
	if mode != "serve" {
		slog.Error("unknown_command")
		os.Exit(1)
	}
	mux := s.routes()
	server := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
	}()
	slog.Info("http_starting", "elapsed_ms", time.Since(started).Milliseconds())
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("http_failed")
		os.Exit(1)
	}
}

func (s *service) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/startup", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /health/ready", s.readiness)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, map[string]any{"service": "deployment-timing", "version": strings.TrimSpace(version), "started_at": s.started.UTC(), "uptime_ms": time.Since(s.started).Milliseconds()})
	})
	mux.Handle("/records/", s.authorize(http.HandlerFunc(s.records)))
	mux.Handle("/objects/", s.authorize(http.HandlerFunc(s.objects)))
	return mux
}

func openService(started time.Time) (*service, error) {
	for _, name := range []string{"DATABASE_URL", "S3_ENDPOINT", "S3_BUCKET", "S3_REGION", "S3_ACCESS_KEY", "S3_SECRET_KEY", "BENCH_TOKEN"} {
		if os.Getenv(name) == "" {
			return nil, fmt.Errorf("missing %s", name)
		}
	}
	db, err := sql.Open("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)
	endpoint, err := url.Parse(os.Getenv("S3_ENDPOINT"))
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		db.Close()
		return nil, errors.New("invalid S3 endpoint")
	}
	s3, err := minio.New(endpoint.Host, &minio.Options{Creds: credentials.NewStaticV4(os.Getenv("S3_ACCESS_KEY"), os.Getenv("S3_SECRET_KEY"), ""), Secure: endpoint.Scheme == "https", Region: os.Getenv("S3_REGION"), BucketLookup: minio.BucketLookupPath})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &service{db: db, s3: s3, bucket: os.Getenv("S3_BUCKET"), token: os.Getenv("BENCH_TOKEN"), started: started}, nil
}

func (s *service) migrate(ctx context.Context) error {
	started := time.Now()
	slog.Info("database_migration_started")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(801008)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS timing_migrations (version INTEGER PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	var applied bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM timing_migrations WHERE version = 1)`).Scan(&applied); err != nil {
		return err
	}
	if !applied {
		if _, err := tx.ExecContext(ctx, migration); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO timing_migrations(version) VALUES (1)`); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	slog.Info("database_migration_completed", "already_applied", applied, "duration_ms", time.Since(started).Milliseconds())
	started = time.Now()
	slog.Info("s3_marker_write_started")
	data := []byte("deployment-timing\n")
	if _, err := s.s3.PutObject(ctx, s.bucket, marker, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{ContentType: "text/plain"}); err != nil {
		return err
	}
	slog.Info("s3_marker_write_completed", "duration_ms", time.Since(started).Milliseconds())
	return nil
}

func (s *service) readiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 1500*time.Millisecond)
	defer cancel()
	var dbErr, s3Err error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		var count int
		dbErr = s.db.QueryRowContext(ctx, `SELECT count(*) FROM timing_migrations WHERE version = 1`).Scan(&count)
		if dbErr == nil && count != 1 {
			dbErr = errors.New("migration missing")
		}
	}()
	go func() { defer wg.Done(); _, s3Err = s.s3.StatObject(ctx, s.bucket, marker, minio.StatObjectOptions{}) }()
	wg.Wait()
	if dbErr != nil || s3Err != nil {
		http.Error(w, "dependencies unavailable", http.StatusServiceUnavailable)
		return
	}
	if s.readyLogged.CompareAndSwap(false, true) {
		slog.Info("dependencies_ready", "elapsed_ms", time.Since(s.started).Milliseconds())
	}
	w.Write([]byte("ok\n"))
}

func (s *service) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestKey(w http.ResponseWriter, r *http.Request, prefix string) (string, bool) {
	key := strings.TrimPrefix(r.URL.Path, prefix)
	if !keyPattern.MatchString(key) {
		http.Error(w, "key must contain 1–80 letters, digits, underscores or hyphens", http.StatusBadRequest)
		return "", false
	}
	return key, true
}

func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		http.Error(w, "invalid or oversized body", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	return data, true
}

func (s *service) records(w http.ResponseWriter, r *http.Request) {
	key, ok := requestKey(w, r, "/records/")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	switch r.Method {
	case http.MethodPut:
		data, ok := readBody(w, r, 64<<10)
		if !ok {
			return
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO timing_records(key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, string(data)); err != nil {
			http.Error(w, "database write failed", 503)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet:
		var value string
		err := s.db.QueryRowContext(ctx, `SELECT value FROM timing_records WHERE key = $1`, key).Scan(&value)
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "database read failed", 503)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(value))
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", 405)
	}
}

func (s *service) objects(w http.ResponseWriter, r *http.Request) {
	key, ok := requestKey(w, r, "/objects/")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	key = "objects/" + key
	switch r.Method {
	case http.MethodPut:
		data, ok := readBody(w, r, 1<<20)
		if !ok {
			return
		}
		if _, err := s.s3.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{ContentType: "application/octet-stream"}); err != nil {
			http.Error(w, "object write failed", 503)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet:
		object, err := s.s3.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
		if err != nil {
			http.Error(w, "object read failed", 503)
			return
		}
		defer object.Close()
		if _, err := object.Stat(); err != nil {
			if minio.ToErrorResponse(err).Code == "NoSuchKey" {
				http.NotFound(w, r)
			} else {
				http.Error(w, "object read failed", 503)
			}
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		io.Copy(w, object)
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", 405)
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(value)
}

// Command file-sharing is the presigned-URL reference application: the
// server authorizes a request and signs an S3 URL, the browser moves the
// bytes straight to the bucket's endpoint, and the file payload never
// passes through the application. Upload records live in the database;
// share links redirect to a short-lived signed GET.
//
// Two subcommands: `serve` runs the HTTP server, `migrate up` creates the
// schema (the manifest runs it as the release command before every
// rollout).
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	_ "github.com/lib/pq"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

//go:embed static
var static embed.FS

const (
	// minPartSize is S3's floor for every multipart part but the last.
	minPartSize = 5 << 20
	maxParts    = 10000
	// maxUploadBytes bounds one upload; the bucket quota is the real limit.
	maxUploadBytes = 5 << 30
	defaultTTL     = 15 * time.Minute
	maxTTL         = 24 * time.Hour
)

type app struct {
	db *sql.DB
	// s3 talks to the in-cluster gateway (S3_INTERNAL_ENDPOINT, falling
	// back to S3_ENDPOINT); signer presigns against the endpoint browsers
	// reach (S3_ENDPOINT, or S3_PUBLIC_ENDPOINT when local development
	// maps the store to a loopback port). Same bucket, same credentials:
	// only the host in the URL differs.
	s3     *minio.Client
	core   *minio.Core
	signer *minio.Client
	bucket string
	token  string
	ttl    time.Duration
}

func main() {
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	switch command {
	case "serve":
		serve()
	case "migrate":
		if len(os.Args) < 3 || os.Args[2] != "up" {
			log.Fatal("usage: file-sharing migrate up")
		}
		db := openDatabase()
		if err := migrate(context.Background(), db); err != nil {
			log.Fatal(err)
		}
		log.Println("schema is up to date")
	default:
		log.Fatalf("unknown command %q (serve, migrate up)", command)
	}
}

func openDatabase() *sql.DB {
	dsn := (&url.URL{
		Scheme: "postgresql",
		User:   url.UserPassword(os.Getenv("POSTGRES_USER"), os.Getenv("POSTGRES_PASSWORD")),
		Host:   os.Getenv("POSTGRES_HOST") + ":" + os.Getenv("POSTGRES_PORT"),
		Path:   "/" + os.Getenv("POSTGRES_DATABASE"),
	}).String()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatal(err)
	}
	return db
}

func migrate(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS files (
			id           TEXT PRIMARY KEY,
			object_key   TEXT NOT NULL UNIQUE,
			name         TEXT NOT NULL,
			content_type TEXT NOT NULL,
			size_bytes   BIGINT NOT NULL,
			upload_id    TEXT,
			status       TEXT NOT NULL,
			created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
			completed_at TIMESTAMPTZ
		);
		CREATE INDEX IF NOT EXISTS files_status_completed_idx
			ON files (status, completed_at DESC)`)
	return err
}

func s3Client(endpoint string) (*minio.Client, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid S3 endpoint %q", endpoint)
	}
	// Path-style addressing and the platform's one region: bucket names
	// are not DNS labels under the endpoint, they are the first path
	// segment, and every signature carries us-east-1.
	return minio.New(parsed.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(os.Getenv("S3_ACCESS_KEY"), os.Getenv("S3_SECRET_KEY"), ""),
		Secure:       parsed.Scheme == "https",
		Region:       os.Getenv("S3_REGION"),
		BucketLookup: minio.BucketLookupPath,
	})
}

func serve() {
	a := &app{
		db:     openDatabase(),
		bucket: os.Getenv("S3_BUCKET"),
		token:  os.Getenv("UPLOAD_TOKEN"),
		ttl:    defaultTTL,
	}
	if a.token == "" {
		log.Fatal("UPLOAD_TOKEN must be set")
	}
	if raw := os.Getenv("PRESIGN_TTL"); raw != "" {
		ttl, err := time.ParseDuration(raw)
		if err != nil || ttl <= 0 || ttl > maxTTL {
			log.Fatalf("PRESIGN_TTL %q: want a duration up to %s", raw, maxTTL)
		}
		a.ttl = ttl
	}
	var err error
	if a.signer, err = s3Client(os.Getenv("S3_ENDPOINT")); err != nil {
		log.Fatal(err)
	}
	if public := os.Getenv("S3_PUBLIC_ENDPOINT"); public != "" {
		if a.signer, err = s3Client(public); err != nil {
			log.Fatal(err)
		}
	}
	a.s3 = a.signer
	if internal := os.Getenv("S3_INTERNAL_ENDPOINT"); internal != "" {
		if a.s3, err = s3Client(internal); err != nil {
			log.Fatal(err)
		}
	}
	a.core = &minio.Core{Client: a.s3}

	pages, err := fs.Sub(static, "static")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", http.FileServerFS(pages))
	mux.HandleFunc("GET /health/startup", ok)
	mux.HandleFunc("GET /health/live", ok)
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if err := a.db.PingContext(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		ok(w, r)
	})
	mux.HandleFunc("POST /api/uploads", a.authorized(a.createUpload))
	mux.HandleFunc("GET /api/uploads", a.authorized(a.listUploads))
	mux.HandleFunc("POST /api/uploads/{id}/complete", a.authorized(a.completeUpload))
	mux.HandleFunc("DELETE /api/uploads/{id}", a.authorized(a.abortUpload))
	mux.HandleFunc("GET /api/files", a.listFiles)
	mux.HandleFunc("GET /api/files/{id}", a.getFile)
	mux.HandleFunc("DELETE /api/files/{id}", a.authorized(a.deleteFile))
	mux.HandleFunc("GET /files/{id}", a.shareLink)

	log.Printf("file-sharing listening on :8080 (bucket %s via %s, signing for %s)",
		a.bucket, a.s3.EndpointURL(), a.signer.EndpointURL())
	log.Fatal(http.ListenAndServe(":8080", mux))
}

func ok(w http.ResponseWriter, _ *http.Request) {
	fmt.Fprintln(w, "ok")
}

// authorized is the request authorization every signing endpoint needs: a
// real application would consult its session; this one checks a bearer
// token from the project environment. Credentials never leave the server
// either way: the browser only ever sees signed URLs.
func (a *app) authorized(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bearer, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !found || subtle.ConstantTimeCompare([]byte(bearer), []byte(a.token)) != 1 {
			writeError(w, http.StatusUnauthorized, "a valid upload token is required")
			return
		}
		next(w, r)
	}
}

type fileRecord struct {
	ID          string     `json:"id"`
	Key         string     `json:"key"`
	Name        string     `json:"name"`
	ContentType string     `json:"contentType"`
	Size        int64      `json:"size"`
	UploadID    string     `json:"uploadId,omitempty"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"createdAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

type uploadRequest struct {
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	// Parts above one requests a multipart upload with that many parts;
	// the browser uploads each part to its own signed URL.
	Parts int `json:"parts"`
	// ExpiresIn shortens the signed URLs' lifetime below the configured
	// default (a Go duration); tests use it to watch a URL expire.
	ExpiresIn string `json:"expiresIn"`
}

type partURL struct {
	PartNumber int    `json:"partNumber"`
	URL        string `json:"url"`
}

type uploadResponse struct {
	ID        string            `json:"id"`
	Key       string            `json:"key"`
	Method    string            `json:"method"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	UploadID  string            `json:"uploadId,omitempty"`
	Parts     []partURL         `json:"parts,omitempty"`
	ExpiresAt time.Time         `json:"expiresAt"`
}

// createUpload records the intent and hands the browser signed PUT URLs:
// one for a single object, one per part for a multipart upload. The
// Content-Type of a single upload is part of the signature, so the browser
// must send exactly what it declared.
func (a *app) createUpload(w http.ResponseWriter, r *http.Request) {
	var req uploadRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	name := strings.TrimSpace(req.Name)
	switch {
	case name == "" || len(name) > 255 || strings.ContainsAny(name, "/\\\x00"):
		writeError(w, http.StatusBadRequest, "name must be a file name without path separators")
		return
	case req.Size <= 0 || req.Size > maxUploadBytes:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("size must be between 1 and %d bytes", maxUploadBytes))
		return
	case req.Parts > maxParts || (req.Parts > 1 && req.Size < int64(req.Parts-1)*minPartSize):
		writeError(w, http.StatusBadRequest, "every part but the last must hold at least 5 MiB")
		return
	}
	contentType := req.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	ttl := a.ttl
	if req.ExpiresIn != "" {
		parsed, err := time.ParseDuration(req.ExpiresIn)
		if err != nil || parsed < time.Second || parsed > a.ttl {
			writeError(w, http.StatusBadRequest, "expiresIn must be a duration between 1s and the configured default")
			return
		}
		ttl = parsed
	}

	id := newID()
	// The name stays in the key verbatim (spaces, brackets, non-ASCII):
	// signing and serving must agree on its URL encoding.
	key := "uploads/" + id + "/" + name
	ctx := r.Context()
	response := uploadResponse{ID: id, Key: key, Method: http.MethodPut, ExpiresAt: time.Now().Add(ttl).UTC()}
	var uploadID string
	if req.Parts > 1 {
		var err error
		uploadID, err = a.core.NewMultipartUpload(ctx, a.bucket, key, minio.PutObjectOptions{ContentType: contentType})
		if err != nil {
			writeError(w, http.StatusBadGateway, "start multipart upload: "+err.Error())
			return
		}
		response.UploadID = uploadID
		for part := 1; part <= req.Parts; part++ {
			signed, err := a.signer.Presign(ctx, http.MethodPut, a.bucket, key, ttl, url.Values{
				"uploadId":   {uploadID},
				"partNumber": {fmt.Sprint(part)},
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "sign part: "+err.Error())
				return
			}
			response.Parts = append(response.Parts, partURL{PartNumber: part, URL: signed.String()})
		}
	} else {
		signed, err := a.signer.PresignHeader(ctx, http.MethodPut, a.bucket, key, ttl, nil,
			http.Header{"Content-Type": {contentType}})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "sign upload: "+err.Error())
			return
		}
		response.URL = signed.String()
		response.Headers = map[string]string{"Content-Type": contentType}
	}

	_, err := a.db.ExecContext(ctx, `
		INSERT INTO files (id, object_key, name, content_type, size_bytes, upload_id, status)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), 'pending')`,
		id, key, name, contentType, req.Size, uploadID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "record upload: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

type completeRequest struct {
	Parts []struct {
		PartNumber int    `json:"partNumber"`
		ETag       string `json:"etag"`
	} `json:"parts"`
}

// completeUpload verifies the object actually landed (and, for multipart,
// assembles it) before the record becomes visible. The declared size must
// match what the bucket holds: a browser cannot claim more than it sent.
func (a *app) completeUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	record, err := a.lookup(ctx, r.PathValue("id"), "pending")
	if err != nil {
		writeLookupError(w, err)
		return
	}
	if record.UploadID != "" {
		var req completeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || len(req.Parts) == 0 {
			writeError(w, http.StatusBadRequest, "a multipart completion lists every part's number and etag")
			return
		}
		parts := make([]minio.CompletePart, len(req.Parts))
		for i, part := range req.Parts {
			parts[i] = minio.CompletePart{PartNumber: part.PartNumber, ETag: strings.Trim(part.ETag, `"`)}
		}
		if _, err := a.core.CompleteMultipartUpload(ctx, a.bucket, record.Key, record.UploadID, parts,
			minio.PutObjectOptions{ContentType: record.ContentType}); err != nil {
			writeError(w, http.StatusConflict, "complete multipart upload: "+err.Error())
			return
		}
	}
	info, err := a.s3.StatObject(ctx, a.bucket, record.Key, minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			writeError(w, http.StatusConflict, "the object has not been uploaded")
			return
		}
		writeError(w, http.StatusBadGateway, "inspect upload: "+err.Error())
		return
	}
	if info.Size != record.Size {
		_ = a.s3.RemoveObject(ctx, a.bucket, record.Key, minio.RemoveObjectOptions{})
		writeError(w, http.StatusConflict, fmt.Sprintf("uploaded %d bytes, declared %d; the object was discarded", info.Size, record.Size))
		return
	}
	if _, err := a.db.ExecContext(ctx, `
		UPDATE files SET status = 'complete', completed_at = now(), upload_id = NULL WHERE id = $1`,
		record.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	record, err = a.lookup(ctx, record.ID, "complete")
	if err != nil {
		writeLookupError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, record)
}

// abortUpload drops a pending record and whatever the bucket holds for it:
// the multipart upload (its parts are freed) or a stray single object.
func (a *app) abortUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	record, err := a.lookup(ctx, r.PathValue("id"), "pending")
	if err != nil {
		writeLookupError(w, err)
		return
	}
	if record.UploadID != "" {
		if err := a.core.AbortMultipartUpload(ctx, a.bucket, record.Key, record.UploadID); err != nil &&
			minio.ToErrorResponse(err).Code != "NoSuchUpload" {
			writeError(w, http.StatusBadGateway, "abort multipart upload: "+err.Error())
			return
		}
	} else if err := a.s3.RemoveObject(ctx, a.bucket, record.Key, minio.RemoveObjectOptions{}); err != nil {
		writeError(w, http.StatusBadGateway, "remove object: "+err.Error())
		return
	}
	if _, err := a.db.ExecContext(ctx, `DELETE FROM files WHERE id = $1`, record.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": record.ID, "status": "aborted"})
}

type pendingUploads struct {
	Records []fileRecord `json:"records"`
	// Multipart lists what the bucket itself still holds open, whether or
	// not a record points at it: abandoned uploads hold space until they
	// are aborted.
	Multipart []map[string]any `json:"multipart"`
}

func (a *app) listUploads(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	records, err := a.list(ctx, "pending")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	result, err := a.core.ListMultipartUploads(ctx, a.bucket, "", "", "", "", 1000)
	if err != nil {
		writeError(w, http.StatusBadGateway, "list multipart uploads: "+err.Error())
		return
	}
	pending := pendingUploads{Records: records, Multipart: []map[string]any{}}
	for _, upload := range result.Uploads {
		pending.Multipart = append(pending.Multipart, map[string]any{
			"key": upload.Key, "uploadId": upload.UploadID, "initiated": upload.Initiated,
		})
	}
	writeJSON(w, http.StatusOK, pending)
}

func (a *app) listFiles(w http.ResponseWriter, r *http.Request) {
	records, err := a.list(r.Context(), "complete")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, records)
}

type fileResponse struct {
	fileRecord
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// getFile signs a download: the URL carries a response-content-disposition
// so the browser saves the file under its original name.
func (a *app) getFile(w http.ResponseWriter, r *http.Request) {
	record, signed, err := a.signDownload(r.Context(), r.PathValue("id"))
	if err != nil {
		writeLookupError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, fileResponse{
		fileRecord: *record, URL: signed.String(), ExpiresAt: time.Now().Add(a.ttl).UTC(),
	})
}

// shareLink is the address people pass around: it never expires itself
// and redirects to a freshly signed GET each time.
func (a *app) shareLink(w http.ResponseWriter, r *http.Request) {
	_, signed, err := a.signDownload(r.Context(), r.PathValue("id"))
	if err != nil {
		writeLookupError(w, err)
		return
	}
	http.Redirect(w, r, signed.String(), http.StatusFound)
}

func (a *app) signDownload(ctx context.Context, id string) (*fileRecord, *url.URL, error) {
	record, err := a.lookup(ctx, id, "complete")
	if err != nil {
		return nil, nil, err
	}
	signed, err := a.signer.PresignedGetObject(ctx, a.bucket, record.Key, a.ttl, url.Values{
		"response-content-disposition": {fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(record.Name))},
	})
	if err != nil {
		return nil, nil, err
	}
	return record, signed, nil
}

func (a *app) deleteFile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	record, err := a.lookup(ctx, r.PathValue("id"), "complete")
	if err != nil {
		writeLookupError(w, err)
		return
	}
	if err := a.s3.RemoveObject(ctx, a.bucket, record.Key, minio.RemoveObjectOptions{}); err != nil {
		writeError(w, http.StatusBadGateway, "remove object: "+err.Error())
		return
	}
	if _, err := a.db.ExecContext(ctx, `DELETE FROM files WHERE id = $1`, record.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": record.ID, "status": "deleted"})
}

var errNotFound = errors.New("no such file")

const selectColumns = `id, object_key, name, content_type, size_bytes, COALESCE(upload_id, ''), status, created_at, completed_at`

func scanRecord(scanner interface{ Scan(...any) error }) (*fileRecord, error) {
	var record fileRecord
	err := scanner.Scan(&record.ID, &record.Key, &record.Name, &record.ContentType, &record.Size,
		&record.UploadID, &record.Status, &record.CreatedAt, &record.CompletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return &record, err
}

func (a *app) lookup(ctx context.Context, id, status string) (*fileRecord, error) {
	return scanRecord(a.db.QueryRowContext(ctx,
		`SELECT `+selectColumns+` FROM files WHERE id = $1 AND status = $2`, id, status))
}

func (a *app) list(ctx context.Context, status string) ([]fileRecord, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT `+selectColumns+` FROM files
		WHERE status = $1 ORDER BY COALESCE(completed_at, created_at) DESC LIMIT 200`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []fileRecord{}
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, *record)
	}
	return records, rows.Err()
}

func newID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw[:])
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeLookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

// Command guestbook is the database + bucket example: every visit inserts
// one row and reports the total, and notes are stored as S3 objects,
// exercising the injected {{databases.data.*}} and {{buckets.files.*}}
// connection outputs end to end.
package main

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	_ "github.com/lib/pq"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func s3Client() (*minio.Client, string, error) {
	endpoint, err := url.Parse(os.Getenv("S3_ENDPOINT"))
	if err != nil {
		return nil, "", fmt.Errorf("parse S3_ENDPOINT: %w", err)
	}
	client, err := minio.New(endpoint.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(os.Getenv("S3_ACCESS_KEY"), os.Getenv("S3_SECRET_KEY"), ""),
		Secure:       endpoint.Scheme == "https",
		Region:       os.Getenv("S3_REGION"),
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return nil, "", err
	}
	return client, os.Getenv("S3_BUCKET"), nil
}

func main() {
	db, err := sql.Open("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS visits (
		id BIGSERIAL PRIMARY KEY,
		at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		log.Fatal(err)
	}
	s3, bucket, err := s3Client()
	if err != nil {
		log.Fatal(err)
	}

	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if err := db.Ping(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := db.Exec(`INSERT INTO visits DEFAULT VALUES`); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var count int64
		if err := db.QueryRow(`SELECT count(*) FROM visits`).Scan(&count); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "visits: %d\n", count)
	})
	// Notes live in the bucket: PUT stores the body as an object, GET reads
	// it back through the same injected credentials.
	http.HandleFunc("/notes/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/notes/")
		if name == "" || strings.Contains(name, "/") {
			http.Error(w, "note name required", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		switch r.Method {
		case http.MethodPut, http.MethodPost:
			_, err := s3.PutObject(ctx, bucket, name, r.Body, r.ContentLength,
				minio.PutObjectOptions{ContentType: "text/plain"})
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			fmt.Fprintf(w, "stored %s\n", name)
		case http.MethodGet:
			object, err := s3.GetObject(ctx, bucket, name, minio.GetObjectOptions{})
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer object.Close()
			if _, err := io.Copy(w, object); err != nil {
				log.Printf("read note %s: %v", name, err)
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	log.Println("guestbook listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

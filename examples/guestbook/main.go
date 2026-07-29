// Command guestbook is the database example: every visit inserts one row
// and reports the total, exercising the injected {{databases.data.*}}
// connection outputs end to end.
package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"

	_ "github.com/lib/pq"
)

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

	log.Println("guestbook listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

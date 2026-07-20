// A minimal HTTP server: the build-sourced counterpart of the hello-world
// example. Everything it does is visible from the outside: a greeting on /
// and a health verdict on /healthz.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	hostname, _ := os.Hostname()
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello from skali (%s)\n", hostname)
	})
	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

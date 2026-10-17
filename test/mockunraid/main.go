// Command mockunraid serves canned Unraid GraphQL responses for end-to-end tests.
package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/firestarter2501/unraid_exporter/internal/mockunraid"
)

func main() {
	addr := flag.String("listen", ":8080", "listen address")
	dir := flag.String("fixtures", "/fixtures", "directory with <OperationName>.json files")
	key := flag.String("api-key", "test-api-key", "expected x-api-key header")
	flag.Parse()
	log.Printf("mock unraid listening on %s, fixtures %s", *addr, *dir)
	srv := &http.Server{Addr: *addr, Handler: mockunraid.Handler(*dir, *key), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

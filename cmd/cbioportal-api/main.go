// cbioportal-api — a cBioPortal-compatible REST API served from the ClickHouse
// transform views. JSON field shapes match the Java backend so the existing
// frontend works unchanged.
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/inodb/cbioportal-go/internal/api"
	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

func main() {
	url := env("CLICKHOUSE_URL", "http://localhost:18123")
	db := env("CBIOPORTAL_DB", "cbioportal")
	port := env("PORT", "8080")

	state := api.NewState(ch.New(url, db))
	handler := api.NewRouter(state)
	addr := "0.0.0.0:" + port
	log.Printf("cbioportal-api on http://macbook-server-1337:%s (clickhouse %s, db %s)", port, url, db)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatal(err)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

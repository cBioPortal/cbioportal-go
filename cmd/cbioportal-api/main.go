// cbioportal-api — a cBioPortal-compatible REST API served from the ClickHouse
// transform views. JSON field shapes match the Java backend so the existing
// frontend works unchanged.
//
// Portal properties (skin.*, priority_studies, quick_search.enabled, …) come
// from the file named by PORTAL_PROPERTIES (application.properties or a legacy
// portal.properties) and from --key=value arguments, which take precedence —
// the same arguments the Java portal is started with.
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/inodb/cbioportal-go/internal/api"
	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
	"github.com/inodb/cbioportal-go/internal/config"
)

func main() {
	url := env("CLICKHOUSE_URL", "http://localhost:18123")
	db := env("CBIOPORTAL_DB", "cbioportal")
	port := env("PORT", "8080")

	client := ch.New(url, db)
	client.SetDatabaseAliases(os.Getenv("CBIOPORTAL_RAW_DB"), os.Getenv("CBIOPORTAL_REF_DB"))
	props, err := config.LoadPortalProperties(os.Getenv("PORTAL_PROPERTIES"), os.Args[1:])
	if err != nil {
		log.Fatalf("portal properties: %v", err)
	}
	state := api.NewState(client)
	state.Props = props
	handler := api.NewRouter(state)
	addr := "0.0.0.0:" + port
	log.Printf("cbioportal-api listening on %s (clickhouse %s, db %s, %d portal properties)", addr, url, db, len(props))
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

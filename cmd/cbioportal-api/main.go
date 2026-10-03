// cbioportal-api — a cBioPortal-compatible REST API served from the ClickHouse
// transform views. JSON field shapes match the Java backend so the existing
// frontend works unchanged.
//
// Server settings are flags with environment-variable fallbacks (see -h).
// Portal properties (skin.*, priority_studies, quick_search.enabled, …) come
// from the file named by -portal-properties (an application.properties or a
// legacy portal.properties) and from --key=value arguments, which take
// precedence — the same arguments the Java portal is started with.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/inodb/cbioportal-go/internal/api"
	"github.com/inodb/cbioportal-go/internal/buildinfo"
	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
	"github.com/inodb/cbioportal-go/internal/config"
)

const name = "cbioportal-api"

func main() {
	cfg, err := parseArgs(os.Args[1:], os.Getenv)
	if errors.Is(err, flag.ErrHelp) {
		printUsage(os.Stdout)
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\nRun '%s -h' for usage.\n", name, err, name)
		os.Exit(2)
	}
	if cfg.showVersion {
		fmt.Println(buildinfo.String(name))
		return
	}

	props, err := config.LoadPortalProperties(cfg.values["portal-properties"], cfg.portalArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: portal properties: %v\n", name, err)
		os.Exit(1)
	}
	// internal/api reads these settings from the environment.
	for _, o := range allOptions() {
		if o.export {
			os.Setenv(o.env, cfg.values[o.name])
		}
	}

	v := cfg.values
	client := ch.New(v["clickhouse-url"], v["db"])
	client.SetDatabaseAliases(v["raw-db"], v["ref-db"])
	state := api.NewState(client)
	state.Props = props
	state.PropsReplaceDefaults = cfg.values["portal-properties"] != ""
	if err := state.ConfigureAuth(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		os.Exit(1)
	}
	handler := api.NewRouter(state)

	addr := "0.0.0.0:" + v["port"]
	log.Printf("%s listening on %s (clickhouse %s, db %s, raw-db %s, ref-db %s)",
		buildinfo.String(name), addr, v["clickhouse-url"], v["db"], v["raw-db"], v["ref-db"])
	log.Print(config.PropertiesSummary(props, 8))
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatal(err)
	}
}

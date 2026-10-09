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
	"log/slog"

	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/KimMachineGun/automemlimit/memlimit"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cbioportal/cbioportal-go/internal/api"
	"github.com/cbioportal/cbioportal-go/internal/buildinfo"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/config"
)

const name = "cbioportal-api"

func main() {
	// Go's soft memory limit at 80% of the container's, unless GOMEMLIMIT is set.
	memlimit.Set(memlimit.WithRatio(0.8), memlimit.WithLogger(slog.Default()))
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
	limits, err := bulkLimits(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\nRun '%s -h' for usage.\n", name, err, name)
		os.Exit(2)
	}
	state := api.NewState(client)
	state.SetBulkLimits(limits)
	state.Props = props
	state.PropsReplaceDefaults = cfg.values["portal-properties"] != ""
	if err := state.ConfigureAuth(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		os.Exit(1)
	}
	if err := state.ConfigureSessions(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		os.Exit(1)
	}
	handler := api.NewRouter(state)

	addr := "0.0.0.0:" + v["port"]
	log.Printf("%s listening on %s (clickhouse %s, db %s, raw-db %s, ref-db %s)",
		buildinfo.String(name), addr, v["clickhouse-url"], v["db"], v["raw-db"], v["ref-db"])
	log.Print(config.PropertiesSummary(props, 8))
	if m := v["metrics-addr"]; m != "" {
		mux := http.NewServeMux()
		mux.Handle("GET /metrics", api.MetricsHandler())
		go func() {
			log.Printf("metrics on %s/metrics", m)
			log.Print(http.ListenAndServe(m, mux))
		}()
	}
	// SIGTERM (a rollout, a scale-down) lets in-flight requests finish and
	// exits 0, within Kubernetes' default 30 s grace period.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	srv := &http.Server{Addr: addr, Handler: handler}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		log.Print("shutting down: draining in-flight requests")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	<-drained
}

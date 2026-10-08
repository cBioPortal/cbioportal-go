package portal

import (
	_ "embed"
	"net/http"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	"github.com/cbioportal/cbioportal-go/internal/api/molecular"
	"github.com/cbioportal/cbioportal-go/internal/buildinfo"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/config"
)

// ConfigService returns the full frontend property set (as the Java
// FrontendPropertiesService does), so feature-gating flags like
// skin_show_settings_menu drive the UI correctly. Runtime bits (auth, session)
// are layered on top of the embedded defaults.
func (s Server) ConfigService(w http.ResponseWriter, r *http.Request) {
	core.WriteJSON(w, s.Config(r))
}

// Config is the frontend configuration for request r's user: the
// /config_service body, which the portal page also embeds.
func (s Server) Config(r *http.Request) map[string]any {
	cfg := config.FrontendConfig("/", s.Props, s.PropsReplaceDefaults)
	cfg["authenticationMethod"] = "noauthsessionservice"
	if s.AuthMethod != "" {
		cfg["authenticationMethod"] = s.AuthMethod
	}
	cfg["sessionServiceEnabled"] = true
	cfg["clickhouse_mode"] = true
	if u, ok := core.GetUser(r.Context()); ok && u.Name != "" {
		cfg["user_display_name"] = u.Name
		cfg["user_email_address"] = u.Name
	}
	return cfg
}

// customDriverAnnotationReport reports whether the given mutation profiles carry
// custom driver annotations (binary cbp_driver and/or tier classes), so the
// settings menu exposes the corresponding filter controls. Values come from the
// mutation, CNA and SV events of genomic_event_derived.
func (s Server) customDriverAnnotationReport(w http.ResponseWriter, r *http.Request) {
	var ids []string
	core.ReadBody(r, &ids)
	type row struct {
		HasBinary uint8    `json:"hasBinary"`
		Tiers     []string `json:"tiers"`
	}
	report := map[string]any{"hasBinary": false, "tiers": []string{}}
	// An empty list reports nothing, as the Java backend answers it.
	if len(ids) == 0 {
		core.WriteJSON(w, report)
		return
	}
	p := ch.Params{}
	rows, _ := ch.QueryP[row](s.CH,
		"SELECT toUInt8(countIf(driver_filter IN ('Putative_Driver', 'Putative_Passenger')) > 0) AS hasBinary, "+
			"arraySort(groupUniqArrayIf(driver_tiers_filter, driver_tiers_filter NOT IN ('', 'NA'))) AS tiers "+
			"FROM "+core.ByProfiles(ids).From(core.ParamsBinder(p), core.TGenomicEventDerived), p)
	if len(rows) > 0 {
		report["hasBinary"] = rows[0].HasBinary == 1
		if len(rows[0].Tiers) > 0 {
			report["tiers"] = rows[0].Tiers
		}
	}
	core.WriteJSON(w, report)
}

// Info is InfoController's response. Versions name what this backend serves:
// the cBioPortal schema / derived-table versions its views reproduce and the
// gene / gene-set tables of the seed database the reference is loaded from.
// Git properties the build does not record are "not set", as in Java.
type Info struct {
	PortalVersion             string `json:"portalVersion"`
	DbVersion                 string `json:"dbVersion"`
	DerivedTableVersion       string `json:"derivedTableVersion"`
	GenesetVersion            string `json:"genesetVersion"`
	GeneTableVersion          string `json:"geneTableVersion"`
	GitBranch                 string `json:"gitBranch"`
	GitCommitID               string `json:"gitCommitId"`
	GitCommitIDDescribe       string `json:"gitCommitIdDescribe"`
	GitCommitIDDescribeShort  string `json:"gitCommitIdDescribeShort"`
	GitCommitMessageFull      string `json:"gitCommitMessageFull"`
	GitCommitMessageShort     string `json:"gitCommitMessageShort"`
	GitCommitMessageUserEmail string `json:"gitCommitMessageUserEmail"`
	GitCommitMessageUserName  string `json:"gitCommitMessageUserName"`
	GitDirty                  bool   `json:"gitDirty"`
}

const (
	portalVersion       = "go-0.1.0"
	dbSchemaVersion     = "2.14.5"
	derivedTableVersion = "1.0.11"
	seedGeneTable       = "hgnc_v7_2025.10.7"
	gitNotSet           = "not set"
)

func (s Server) info(w http.ResponseWriter, r *http.Request) {
	info := Info{
		PortalVersion: portalVersion, DbVersion: dbSchemaVersion, DerivedTableVersion: derivedTableVersion,
		GenesetVersion: molecular.SeedGenesetVersion, GeneTableVersion: seedGeneTable,
		GitBranch: gitNotSet, GitCommitID: gitNotSet, GitCommitIDDescribe: gitNotSet, GitCommitIDDescribeShort: gitNotSet,
		GitCommitMessageFull: gitNotSet, GitCommitMessageShort: gitNotSet,
		GitCommitMessageUserEmail: gitNotSet, GitCommitMessageUserName: gitNotSet,
	}
	if rev, modified := buildinfo.Commit(); rev != "" {
		info.GitCommitID = rev
		info.GitCommitIDDescribe = rev[:min(7, len(rev))]
		info.GitDirty = modified
		if modified {
			info.GitCommitIDDescribe += "-dirty"
		}
		info.GitCommitIDDescribeShort = info.GitCommitIDDescribe
	}
	core.WriteJSON(w, info)
}

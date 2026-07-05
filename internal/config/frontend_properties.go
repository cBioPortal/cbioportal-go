package config

import (
	"bufio"
	"io"
	"log/slog"
	"os"
	"strings"
)

// FrontendPropertyDef maps an application.properties key to a frontend config name.
type FrontendPropertyDef struct {
	PropertyName string // key in application.properties
	FrontendName string // key returned in /config_service JSON
	DefaultValue *string
}

func strPtr(s string) *string { return &s }

// FrontendProperties defines all properties exposed to the frontend, matching
// the Java FrontendProperty enum in FrontendPropertiesServiceImpl.java.
// Format: {PropertyName, FrontendName, DefaultValue}.
var FrontendProperties = []FrontendPropertyDef{
	{"dat.method", "dat_method", nil},
	{"oncoprint.custom_driver_annotation.binary.menu_label", "oncoprint_custom_driver_annotation_binary_menu_label", nil},
	{"oncoprint.custom_driver_annotation.binary.menu_description", "oncoprint_custom_driver_annotation_binary_menu_description", nil},
	{"disabled_tabs", "disabled_tabs", nil},
	{"civic.url", "civic_url", nil},
	{"oncoprint.custom_driver_annotation.binary.default", "oncoprint_custom_driver_annotation_binary_default", nil},
	{"oncoprint.oncokb.default", "oncoprint_oncokb_default", nil},
	{"oncoprint.hotspots.default", "oncoprint_hotspots_default", nil},
	{"oncoprint.defaultview", "oncoprint_defaultview", nil},
	{"oncoprint.clustered.default", "oncoprint_clustered_default", nil},
	{"oncokb.merge_icons_by_default", "oncokb_merge_icons_by_default", nil},
	{"oncoprint.clinical_tracks.config_json", "oncoprint_clinical_tracks_config_json", nil},
	{"genomenexus.url", "genomenexus_url", nil},
	{"genomenexus.url.grch38", "genomenexus_url_grch38", nil},
	{"genomenexus.isoform_override_source", "genomenexus_isoform_override_source", nil},
	{"google_analytics_profile_id", "google_analytics_profile_id", nil},
	{"google_tag_manager_id", "google_tag_manager_id", nil},
	{"datadog_rum_application_id", "datadog_rum_application_id", nil},
	{"datadog_rum_client_token", "datadog_rum_client_token", nil},
	{"datadog_rum_site", "datadog_rum_site", nil},
	{"datadog_rum_env", "datadog_rum_env", nil},
	{"datadog_rum_session_sample_rate", "datadog_rum_session_sample_rate", nil},
	{"datadog_rum_session_replay_sample_rate", "datadog_rum_session_replay_sample_rate", nil},
	{"analytics_report_url", "analytics_report_url", nil},
	{"oncoprint.hide_vus.default", "oncoprint_hide_vus_default", nil},
	{"oncokb.public_api.url", "oncokb_public_api_url", nil},
	{"digitalslidearchive.iframe.url", "digitalslidearchive_iframe_url", nil},
	{"digitalslidearchive.meta.url", "digitalslidearchive_meta_url", nil},
	{"mdacc.heatmap.meta.url", "mdacc_heatmap_meta_url", nil},
	{"mdacc.heatmap.patient.url", "mdacc_heatmap_patient_url", nil},
	{"mdacc.heatmap.study.meta.url", "mdacc_heatmap_study_meta_url", nil},
	{"mdacc.heatmap.study.url", "mdacc_heatmap_study_url", nil},
	{"show.mdacc.heatmap", "show_mdacc_heatmap", nil},
	{"oncoprint.custom_driver_annotation.tiers.menu_label", "oncoprint_custom_driver_annotation_tiers_menu_label", nil},
	{"oncoprint.custom_driver_annotation.tiers.menu_description", "oncoprint_custom_driver_annotation_tiers_menu_description", nil},
	{"patient_view.use_legacy_timeline", "patient_view_use_legacy_timeline", nil},
	{"installation_map_url", "installation_map_url", nil},
	{"priority_studies", "priority_studies", nil},
	{"show.hotspot", "show_hotspot", nil},
	{"show.oncokb", "show_oncokb", nil},
	{"show.civic", "show_civic", nil},
	{"show.genomenexus", "show_genomenexus", nil},
	{"show.genomenexus.annotation_sources", "show_genomenexus_annotation_sources", nil},
	{"show.mutation_mappert_tool.grch38", "show_mutation_mappert_tool_grch38", nil},
	{"show.transcript_dropdown", "show_transcript_dropdown", nil},
	{"show.signal", "show_signal", nil},
	{"show.cbioportal", "show_cbioportal", nil},
	{"show.ndex", "show_ndex", nil},
	{"survival.show_p_q_values_in_survival_type_table", "survival_show_p_q_values_in_survival_type_table", nil},
	{"survival.min_group_threshold", "survival_min_group_threshold", nil},
	{"survival.initial_x_axis_limit", "survival_initial_x_axis_limit", nil},
	{"skin.documentation.about", "skin_documentation_about", nil},
	{"skin.documentation.baseurl", "skin_documentation_baseurl", nil},
	{"skin.example_study_queries", "skin_example_study_queries", nil},
	{"skin.blurb", "skin_blurb", nil},
	{"skin.custom_header_tabs", "skin_custom_header_tabs", nil},
	{"skin.data_sets_footer", "skin_data_sets_footer", nil},
	{"skin.data_sets_header", "skin_data_sets_header", nil},
	{"skin.documentation.markdown", "skin_documentation_markdown", nil},
	{"skin.email_contact", "skin_email_contact", nil},
	{"skin.examples_right_column_html", "skin_examples_right_column_html", nil},
	{"skin.documentation.faq", "skin_documentation_faq", nil},
	{"skin.footer", "skin_footer", nil},
	{"skin.footer_show_dev", "skin_footer_show_dev", nil},
	{"skin.login.contact_html", "skin_login_contact_html", nil},
	{"skin.login.saml.registration_html", "skin_login_saml_registration_html", nil},
	{"skin.documentation.news", "skin_documentation_news", nil},
	{"skin.documentation.oql", "skin_documentation_oql", nil},
	{"skin.query.max_tree_depth", "skin_query_max_tree_depth", nil},
	{"skin.quick_select_buttons", "skin_quick_select_buttons", nil},
	{"skin.right_logo", "skin_right_logo", nil},
	{"skin.left_logo", "skin_left_logo", nil},
	{"skin.right_nav.show_data_sets", "skin_right_nav_show_data_sets", nil},
	{"skin.right_nav.show_examples", "skin_right_nav_show_examples", nil},
	{"skin.right_nav.show_testimonials", "skin_right_nav_show_testimonials", nil},
	{"skin.right_nav.show_whats_new", "skin_right_nav_show_whats_new", nil},
	{"skin.right_nav.show_twitter", "skin_right_nav_show_twitter", nil},
	{"skin.right_nav.whats_new_blurb", "skin_right_nav_whats_new_blurb", nil},
	{"skin.show_about_tab", "skin_show_about_tab", nil},
	{"skin.show_data_tab", "skin_show_data_tab", nil},
	{"skin.show_faqs_tab", "skin_show_faqs_tab", nil},
	{"skin.show_news_tab", "skin_show_news_tab", nil},
	{"skin.show_r_matlab_tab", "skin_show_r_matlab_tab", nil},
	{"skin.show_tools_tab", "skin_show_tools_tab", nil},
	{"skin.show_tutorials_tab", "skin_show_tutorials_tab", nil},
	{"skin.show_web_api_tab", "skin_show_web_api_tab", nil},
	{"skin.show_tweet_button", "skin_show_tweet_button", nil},
	{"skin.show_study_help_button", "skin_show_study_help_button", nil},
	{"skin.patientview.filter_genes_profiled_all_samples", "skin_patientview_filter_genes_profiled_all_samples", nil},
	{"skin.patientview.show_mskcc_slide_viewer", "skin_patientview_show_mskcc_slide_viewer", nil},
	{"skin.show_settings_menu", "skin_show_settings_menu", nil},
	{"skin.hide_logout_button", "skin_hide_logout_button", nil},
	{"quick_search.enabled", "quick_search_enabled", nil},
	{"default_cross_cancer_study_session_id", "default_cross_cancer_study_session_id", nil},
	{"default_cross_cancer_study_list", "default_cross_cancer_study_list", nil},
	{"default_cross_cancer_study_list_name", "default_cross_cancer_study_list_name", nil},
	{"skin.description", "skin_description", nil},
	{"skin.title", "skin_title", nil},
	{"app.name", "app_name", nil},
	{"skin.authorization_message", "skin_authorization_message", nil},
	{"session.url_length_threshold", "session_url_length_threshold", nil},
	{"bitly.api_key", "bitly_api_key", nil},
	{"bitly.user", "bitly_user", nil},
	{"bitly.access.token", "bitly_access_token", nil},
	{"oncoprint.custom_driver_annotation.tiers.default", "oncoprint_custom_driver_annotation_tiers_default", nil},
	{"ensembl.transcript_url", "ensembl_transcript_url", nil},
	{"enable_persistent_cache", "enable_persistent_cache", nil},
	{"enable_request_body_gzip_compression", "enable_request_body_gzip_compression", nil},
	{"query_product_limit", "query_product_limit", nil},
	{"skin.show_gsva", "skin_show_gsva", nil},
	{"saml.idp.metadata.entityid", "saml_idp_metadata_entityid", nil},
	{"saml.logout.local", "saml_logout_local", nil},
	{"skin.citation_rule_text", "skin_citation_rule_text", nil},
	{"skin.geneset_hierarchy.default_p_value", "skin_geneset_hierarchy_default_p_value", nil},
	{"skin.geneset_hierarchy.default_gsva_score", "skin_geneset_hierarchy_default_gsva_score", nil},
	{"app.version", "app_version", nil},
	{"sentryjs.frontend_project_endpoint", "frontendSentryEndpoint", nil},
	// Properties that require additional processing in Java.
	// For the Go implementation, we handle them with special logic.
	{"frontend.config", "frontendConfigOverride", nil},
	{"querypage.setsofgenes.location", "query_sets_of_genes", nil},
	{"authenticate", "authenticationMethod", strPtr("false")},
	{"msk.whole.slide.viewer.secret.key", "mskWholeSlideViewerToken", nil},
	{"oncoprint.oncokb_hotspots.default", "oncoprintOncoKbHotspotsDefault", strPtr("true")},
	{"oncokb.token", "oncoKbTokenDefined", strPtr("")},
	{"session.service.url", "sessionServiceEnabled", strPtr("")},
	{"frontend.url", "frontendUrl", nil},
	{"skin.hide_download_controls", "skin_hide_download_controls", strPtr("show")},
	{"study_download_url", "study_download_url", strPtr("https://datahub.assets.cbioportal.org/")},
	{"enable_cross_study_expression", "enable_cross_study_expression", strPtr("")},
	{"studyview.max_samples_selected", "studyview_max_samples_selected", nil},
	{"skin.home_page.show_reference_genome", "skin_home_page_show_reference_genome", nil},
	{"vaf.sequential_mode.default", "vaf_sequential_mode_default", nil},
	{"vaf.log_scale.default", "vaf_log_scale_default", nil},
	{"skin.patient_view.custom_sample_type_colors_json", "skin_patient_view_custom_sample_type_colors_json", nil},
	{"skin.study_view.show_sv_table", "skin_study_view_show_sv_table", nil},
	{"skin.home_page.show_unauthorized_studies", "skin_home_page_show_unauthorized_studies", nil},
	{"skin.home_page.unauthorized_studies_global_message", "skin_home_page_unauthorized_studies_global_message", nil},
	{"skin.mutation_table.namespace_column.show_by_default", "skin_mutation_table_namespace_column_show_by_default", nil},
	{"skin.geneset_hierarchy.collapse_by_default", "skin_geneset_hierarchy_collapse_by_default", nil},
	{"skin.patient_view.mutation_table.columns.show_on_init", "skin_patient_view_mutation_table_columns_show_on_init", nil},
	{"skin.results_view.mutation_table.columns.show_on_init", "skin_results_view_mutation_table_columns_show_on_init", nil},
	{"skin.patient_view.copy_number_table.columns.show_on_init", "skin_patient_view_copy_number_table_columns_show_on_init", nil},
	{"skin.patient_view.structural_variant_table.columns.show_on_init", "skin_patient_view_structural_variant_table_columns_show_on_init", nil},
	{"skin.results_view.tables.default_sort_column", "skin_results_view_tables_default_sort_column", nil},
	{"skin.survival_plot.clinical_event_types.show_on_init", "skin_survival_plot_clinical_event_types_show_on_init", nil},
	{"skin.show_donate_button", "skin_show_donate_button", strPtr("false")},
	{"skin.patient_view.tables.default_sort_column", "skin_patient_view_tables_default_sort_column", nil},
	{"enable_treatment_groups", "enable_treatment_groups", nil},
	{"comparison.categorical_na_values", "comparison_categorical_na_values", nil},
	{"clinical_attribute_product_limit", "clinical_attribute_product_limit", nil},
	{"skin.right_nav.show_web_tours", "skin_right_nav_show_web_tours", strPtr("false")},
	{"download_custom_buttons_json", "download_custom_buttons_json", nil},
	{"enable_study_tags", "enable_study_tags", nil},
	{"clickhouse_mode", "clickhouse_mode", strPtr("false")},
	{"feature.study.export", "feature_study_export", strPtr("false")},
}

// specialFrontendNames lists frontend property names that require special
// processing (matching the Java switch statement).
var specialFrontendNames = map[string]bool{
	"frontendConfigOverride":                     true, // readFile
	"query_sets_of_genes":                        true, // readFile
	"skin_patient_view_custom_sample_type_colors_json": true, // readFile
	"oncoprint_clinical_tracks_config_json":      true, // readFile
	"download_custom_buttons_json":               true, // readFile
	"oncoprintOncoKbHotspotsDefault":             true, // enableOncoKBandHotspotsParamValue
	"oncoKbTokenDefined":                         true, // non-empty check -> bool string
	"sessionServiceEnabled":                      true, // non-empty check -> bool string
	"frontendUrl":                                true, // URL normalization
	"mskWholeSlideViewerToken":                   true, // set at runtime, not from props
}

// ParseApplicationProperties reads a Java-style application.properties file.
// Supports # comments, blank lines, key=value and key: value formats,
// and backslash line continuations.
func ParseApplicationProperties(r io.Reader) map[string]string {
	props := make(map[string]string)
	scanner := bufio.NewScanner(r)

	var currentKey string
	var currentVal strings.Builder
	continuing := false

	for scanner.Scan() {
		line := scanner.Text()

		if continuing {
			trimmed := strings.TrimSpace(line)
			if strings.HasSuffix(trimmed, `\`) {
				currentVal.WriteString(trimmed[:len(trimmed)-1])
			} else {
				currentVal.WriteString(trimmed)
				props[currentKey] = currentVal.String()
				continuing = false
			}
			continue
		}

		trimmed := strings.TrimSpace(line)
		// Skip comments and blank lines
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "!") {
			continue
		}

		// Find separator (= or :)
		sepIdx := -1
		for i, ch := range trimmed {
			if ch == '=' || ch == ':' {
				// Make sure it's not escaped
				if i > 0 && trimmed[i-1] == '\\' {
					continue
				}
				sepIdx = i
				break
			}
		}

		if sepIdx < 0 {
			// Key with no value
			props[trimmed] = ""
			continue
		}

		key := strings.TrimSpace(trimmed[:sepIdx])
		val := strings.TrimSpace(trimmed[sepIdx+1:])

		if strings.HasSuffix(val, `\`) {
			currentKey = key
			currentVal.Reset()
			currentVal.WriteString(val[:len(val)-1])
			continuing = true
			continue
		}

		props[key] = val
	}

	// Handle case where file ends mid-continuation
	if continuing {
		props[currentKey] = currentVal.String()
	}

	return props
}

// ParseApplicationPropertiesFile reads properties from a file path.
func ParseApplicationPropertiesFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseApplicationProperties(f), nil
}

// readFileContent reads a file and returns its content as a single-line string
// (matching the Java readFile behavior: each line trimmed and joined).
func readFileContent(filePath string) string {
	if filePath == "" {
		return ""
	}
	// Strip classpath: prefix
	filePath = strings.TrimPrefix(filePath, "classpath:")

	f, err := os.Open(filePath)
	if err != nil {
		slog.Warn("could not read frontend config file", "path", filePath, "error", err)
		return ""
	}
	defer f.Close()

	var sb strings.Builder
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		sb.WriteString(strings.TrimSpace(scanner.Text()))
	}
	return sb.String()
}

// enableOncoKBandHotspotsParamValue matches the Java logic for
// oncoprintOncoKbHotspotsDefault processing.
func enableOncoKBandHotspotsParamValue(val string) string {
	lower := strings.ToLower(val)
	switch {
	case lower == "false":
		return `"disable"`
	case lower == "custom":
		return `"custom"`
	default:
		return "undefined"
	}
}

// normalizeFrontendURL ensures the URL ends with /.
func normalizeFrontendURL(val string) string {
	if val == "" {
		return ""
	}
	val = strings.TrimSpace(val)
	if val != "" && !strings.HasSuffix(val, "/") {
		val += "/"
	}
	return val
}

// BuildFrontendConfig builds the map[string]interface{} that the
// /config_service endpoint returns, matching the Java backend behavior.
// appProps is the parsed application.properties map. baseURL is the
// server's base URL to include as "base_url".
func BuildFrontendConfig(appProps map[string]string, baseURL string) map[string]interface{} {
	result := make(map[string]interface{}, len(FrontendProperties)+3)

	for _, prop := range FrontendProperties {
		val, ok := appProps[prop.PropertyName]
		if !ok {
			if prop.DefaultValue != nil {
				val = *prop.DefaultValue
			} else {
				// Property not set and no default: null in JSON
				result[prop.FrontendName] = nil
				continue
			}
		}
		val = strings.TrimSpace(val)

		// Apply special processing matching the Java switch cases
		switch prop.FrontendName {
		case "frontendConfigOverride", "query_sets_of_genes",
			"skin_patient_view_custom_sample_type_colors_json",
			"oncoprint_clinical_tracks_config_json",
			"download_custom_buttons_json":
			content := readFileContent(val)
			if content == "" {
				result[prop.FrontendName] = nil
			} else {
				result[prop.FrontendName] = content
			}
			continue

		case "oncoprintOncoKbHotspotsDefault":
			result[prop.FrontendName] = enableOncoKBandHotspotsParamValue(val)
			continue

		case "oncoKbTokenDefined", "sessionServiceEnabled":
			result[prop.FrontendName] = val != ""
			continue

		case "frontendUrl":
			result[prop.FrontendName] = normalizeFrontendURL(val)
			continue

		case "mskWholeSlideViewerToken":
			// This is generated at runtime per-request in the Java backend.
			// For the Go API we set it to nil.
			result[prop.FrontendName] = nil
			continue
		}

		// Convert "true"/"false" strings to boolean values
		lower := strings.ToLower(val)
		if lower == "true" {
			result[prop.FrontendName] = true
		} else if lower == "false" {
			result[prop.FrontendName] = false
		} else {
			result[prop.FrontendName] = val
		}
	}

	// Additional runtime properties
	result["base_url"] = baseURL
	result["user_email_address"] = "anonymousUser"
	result["user_display_name"] = "anonymousUser"

	return result
}

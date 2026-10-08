package config

import "testing"

func TestGenericAssayDisplayText(t *testing.T) {
	cfg := FrontendConfig("/", map[string]string{"generic_assay_display_text": "Treatment"}, true)
	if cfg["generic_assay_display_text"] != "Treatment" {
		t.Errorf("generic_assay_display_text = %v", cfg["generic_assay_display_text"])
	}
	if v, ok := FrontendConfig("/", nil, true)["generic_assay_display_text"]; !ok || v != nil {
		t.Errorf("unset generic_assay_display_text = %v (present %v), want null", v, ok)
	}
}

func TestOncoKBTokenDefined(t *testing.T) {
	t.Setenv("ONCOKB_TOKEN", "")
	if cfg := FrontendConfig("/", nil, true); cfg["oncoKbTokenDefined"] != false {
		t.Errorf("no token: oncoKbTokenDefined = %v", cfg["oncoKbTokenDefined"])
	}
	if cfg := FrontendConfig("/", map[string]string{"oncokb.token": "abc"}, true); cfg["oncoKbTokenDefined"] != true {
		t.Errorf("property: oncoKbTokenDefined = %v", cfg["oncoKbTokenDefined"])
	}
	t.Setenv("ONCOKB_TOKEN", "from-env")
	if cfg := FrontendConfig("/", nil, true); cfg["oncoKbTokenDefined"] != true {
		t.Errorf("environment: oncoKbTokenDefined = %v", cfg["oncoKbTokenDefined"])
	}
	if got := OncoKBToken(map[string]string{"oncokb.token": "prop"}); got != "from-env" {
		t.Errorf("OncoKBToken = %q, want the environment's", got)
	}
}

func TestSkinHideDownloadControls(t *testing.T) {
	for in, want := range map[string]string{"true": "hide", "FALSE": "show", "data": "data", "hide": "hide"} {
		cfg := FrontendConfig("/", map[string]string{"skin.hide_download_controls": in}, true)
		if cfg["skin_hide_download_controls"] != want {
			t.Errorf("%s: skin_hide_download_controls = %v, want %q", in, cfg["skin_hide_download_controls"], want)
		}
	}
	if cfg := FrontendConfig("/", nil, true); cfg["skin_hide_download_controls"] != "show" {
		t.Errorf("default skin_hide_download_controls = %v", cfg["skin_hide_download_controls"])
	}
}

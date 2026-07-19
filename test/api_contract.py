#!/usr/bin/env python3
"""Fast API-contract tests for cbioportal-go.

Asserts the exact endpoint behaviours the frontend/e2e suite depends on, in
seconds and with no browser. Run against a live API:

    python3 test/api_contract.py                # default http://localhost:18090
    API=http://localhost:18091 python3 test/api_contract.py
    python3 test/api_contract.py -k alteration  # only tests whose name matches

Each check is a function decorated with @check; it POSTs/GETs and asserts.
Failures print a concise diff. Exit code is nonzero if any check fails, so it
doubles as a CI gate. Seed it whenever a backend contract is nailed down so the
next refactor gets instant feedback instead of a 45-minute e2e run.
"""
import gzip
import json
import os
import sys
import urllib.request

API = os.environ.get("API", "http://localhost:18090")
CHECKS = []


def check(fn):
    CHECKS.append(fn)
    return fn


def _req(method, path, body=None, gz=False, headers=None):
    data = None
    h = {"Content-Type": "application/json"}
    if headers:
        h.update(headers)
    if body is not None:
        raw = json.dumps(body).encode()
        if gz:
            raw = gzip.compress(raw)
            h["Content-Encoding"] = "gzip"
        data = raw
    req = urllib.request.Request(API + path, data=data, headers=h, method=method)
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.load(r)


def POST(path, body, **kw):
    return _req("POST", path, body, **kw)


def GET(path, **kw):
    return _req("GET", path, **kw)


def gene_counts(rows, field="totalCount"):
    return {r["hugoGeneSymbol"]: r[field] for r in rows}


def eq(got, want, msg=""):
    assert got == want, f"{msg}\n  want: {want}\n  got:  {got}"


# --- reusable alteration filters (mirrors what the study view sends) ---
ALL_TIERS = {"Class 1": True, "Class 2": True, "Class 3": True, "Class 4": True}


def af(**over):
    base = dict(includeDriver=True, includeVUS=True, includeUnknownOncogenicity=True,
               includeGermline=True, includeSomatic=True, includeUnknownStatus=True,
               includeUnknownTier=True, tiersBooleanMap=ALL_TIERS)
    base.update(over)
    return base


# ---------------------------------------------------------------- config ----
@check
def config_service_gating_flags():
    d = GET("/config_service")
    assert len(d) > 100, f"config too small: {len(d)} keys"
    for k, v in {"skin_show_settings_menu": True, "show_oncokb": True,
                 "show_hotspot": True, "skin_show_gsva": True}.items():
        eq(d.get(k), v, f"config {k}")


# --------------------------------------------------- sample/patient forms ----
@check
def samples_fetch_by_sample_list():
    d = POST("/api/samples/fetch?projection=DETAILED", {"sampleListIds": ["study_es_0_all"]})
    assert len(d) == 845, f"expected 845 samples, got {len(d)}"


@check
def patients_fetch_by_unique_keys():
    import base64
    key = base64.b64encode(b"study_es_0:TCGA-A1-A0SB").decode().rstrip("=")
    d = POST("/api/patients/fetch", {"uniquePatientKeys": [key]})
    eq([p["patientId"] for p in d], ["TCGA-A1-A0SB"], "patients by uniquePatientKeys")


@check
def mutations_fetch_by_profile_ids():
    d = POST("/api/mutations/fetch?projection=DETAILED",
             {"molecularProfileIds": ["study_es_0_mutations"], "entrezGeneIds": [3983, 7157]})
    assert len(d) > 0, "mutations/fetch molecularProfileIds returned []"
    assert any(m.get("driverFilter") for m in d), "no driverFilter on mutations"


@check
def gzip_request_body():
    # large body sent gzip-compressed must be decoded (frontend does this)
    import base64
    key = base64.b64encode(b"study_es_0:TCGA-A1-A0SB").decode().rstrip("=")
    d = POST("/api/patients/fetch", {"uniquePatientKeys": [key]}, gz=True)
    eq(len(d), 1, "gzipped patients/fetch")


# ---------------------------------------------------- molecular profiles ----
@check
def molecular_profiles_shape():
    d = GET("/api/studies/study_es_0/molecular-profiles")
    by = {p["molecularProfileId"]: p for p in d}
    eq(by["study_es_0_gistic"]["datatype"], "DISCRETE", "gistic datatype normalized")
    ga = [p for p in d if "GENERIC_ASSAY" in (p.get("molecularAlterationType") or "")]
    assert all(p.get("genericAssayType") for p in ga), "generic-assay profiles missing genericAssayType"


@check
def study_has_cancer_type_object():
    d = GET("/api/studies/study_es_0")
    assert d.get("cancerType") and d["cancerType"].get("name"), "study missing nested cancerType.name"


@check
def gene_panels_fetch_post():
    d = POST("/api/gene-panels/fetch", ["TESTPANEL1", "TESTPANEL2"])
    by = {p["genePanelId"]: len(p.get("genes", [])) for p in d}
    assert by.get("TESTPANEL1", 0) > 0 and by.get("TESTPANEL2", 0) > 0, f"gene-panels/fetch: {by}"


@check
def gene_panel_data_whole_genome_profiled():
    d = POST("/api/gene-panel-data/fetch", {"molecularProfileIds": ["study_es_0_mutations"]})
    profiled = sum(1 for x in d if x.get("profiled"))
    assert profiled > 800, f"expected most samples profiled (whole-genome), got {profiled}/{len(d)}"


# ------------------------------------------- study-view alteration filter ----
@check
def mutated_genes_exclude_somatic():
    d = POST("/api/mutated-genes/fetch", {"studyIds": ["study_es_0"], "alterationFilter": af(includeSomatic=False)})
    eq(gene_counts(d), {"BRCA2": 6, "TP53": 1, "BRCA1": 1, "ATM": 1}, "mutated-genes exclude somatic")


@check
def mutated_genes_exclude_driver():
    d = POST("/api/mutated-genes/fetch", {"studyIds": ["study_es_0"], "alterationFilter": af(includeDriver=False)})
    want = {"BRCA2": 12, "ACP3": 5, "BRCA1": 3, "DTNB": 1, "TP53": 2, "TMEM247": 1,
            "ADAMTS20": 1, "MYB": 1, "MSH3": 1, "ATM": 2, "PIEZO1": 1}
    eq(gene_counts(d), want, "mutated-genes exclude driver (binary/tier separated)")


@check
def cna_genes_deep_events_only():
    d = POST("/api/cna-genes/fetch", {"studyIds": ["study_es_0"]})
    eq(sorted({r["alteration"] for r in d}), [-2, 2], "cna-genes deep events only")


@check
def cna_genes_exclude_driver():
    d = POST("/api/cna-genes/fetch", {"studyIds": ["study_es_0"], "alterationFilter": af(includeDriver=False)})
    amp = {r["hugoGeneSymbol"]: r["numberOfAlteredCases"] for r in d if r["alteration"] == 2}
    eq(amp.get("ERCC5"), 6, "cna AMP ERCC5 excl-driver")
    eq(amp.get("AGRN"), 6, "cna AMP AGRN excl-driver")
    eq(amp.get("ATAD3A"), 7, "cna AMP ATAD3A excl-driver")


@check
def sv_genes_status_filter():
    somatic_off = POST("/api/structuralvariant-genes/fetch",
                       {"studyIds": ["study_es_0"], "alterationFilter": af(includeSomatic=False)})
    eq(len(somatic_off), 0, "SV genes empty when somatic excluded (all SVs somatic)")
    germ_off = POST("/api/structuralvariant-genes/fetch",
                    {"studyIds": ["study_es_0"], "alterationFilter": af(includeGermline=False)})
    braf = sum(r["totalCount"] for r in germ_off if "BRAF" in (r.get("hugoGeneSymbols") or []))
    eq(braf, 37, "SV BRAF total when germline excluded")


@check
def alteration_enrichments_profile_typed():
    body = {"molecularProfileCasesGroupFilter": [
        {"name": "a", "molecularProfileCaseIdentifiers": [
            {"caseId": "TCGA-A2-A04P-01", "molecularProfileId": "study_es_0_mutations"},
            {"caseId": "TCGA-A1-A0SB-01", "molecularProfileId": "study_es_0_mutations"}]},
        {"name": "b", "molecularProfileCaseIdentifiers": [
            {"caseId": "TCGA-A1-A0SK-01", "molecularProfileId": "study_es_0_mutations"}]}]}
    d = POST("/api/alteration-enrichments/fetch", body)
    cna = [r["hugoGeneSymbol"] for r in d if r["hugoGeneSymbol"] in
           ("ACAP3", "AGRN", "ATAD3A", "ATAD3B", "ATAD3C", "AURKAIP1", "ERCC5")]
    eq(cna, [], "mutations-profile enrichment must not include CNA genes")


def main():
    only = None
    if "-k" in sys.argv:
        only = sys.argv[sys.argv.index("-k") + 1]
    checks = [c for c in CHECKS if not only or only in c.__name__]
    p = f = 0
    for c in checks:
        try:
            c()
            print(f"  PASS  {c.__name__}")
            p += 1
        except Exception as e:
            print(f"  FAIL  {c.__name__}: {e}")
            f += 1
    print(f"\n{p} passed / {f} failed ({len(checks)} checks) against {API}")
    sys.exit(1 if f else 0)


if __name__ == "__main__":
    main()

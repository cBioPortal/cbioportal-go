#!/usr/bin/env python3
"""Summarize a Playwright run from its JSON report.

The e2e config writes test-results/report.json on every run (reporter: json).
Parsing that is far more reliable than scraping the line reporter's stdout,
which does not flush when the run is backgrounded (0-byte logs). Usage:

    python3 test/e2e_report.py [path-to-report.json]
      # default: cbioportal-frontend/end-to-end-test-playwright/test-results/report.json
    python3 test/e2e_report.py --fails   # only list failing tests
    python3 test/e2e_report.py --by-spec # failed-count grouped by spec file
"""
import json
import os
import sys

DEFAULT = os.path.expanduser(
    "~/projects/cbioportal-rust/cbioportal-frontend/end-to-end-test-playwright/test-results/report.json"
)


def walk(suite, out, file=None):
    file = suite.get("file", file)
    for sp in suite.get("specs", []):
        status = "passed" if all(t.get("status") == "expected" for t in sp.get("tests", [])) else \
            "skipped" if all(t.get("status") == "skipped" for t in sp.get("tests", [])) else "failed"
        out.append((file, sp.get("title", "?"), status))
    for s in suite.get("suites", []):
        walk(s, out, file)


def main():
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    flags = {a for a in sys.argv[1:] if a.startswith("--")}
    path = args[0] if args else DEFAULT
    if not os.path.exists(path):
        print(f"no report at {path} (run the suite without --reporter so the config's json reporter writes it)")
        sys.exit(2)
    d = json.load(open(path))
    out = []
    for s in d.get("suites", []):
        walk(s, out)
    p = sum(1 for _, _, r in out if r == "passed")
    f = sum(1 for _, _, r in out if r == "failed")
    sk = sum(1 for _, _, r in out if r == "skipped")

    if "--by-spec" in flags:
        from collections import Counter
        c = Counter(os.path.basename(fl or "?") for fl, _, r in out if r == "failed")
        for spec, n in c.most_common():
            print(f"  {n:3}  {spec}")
    if "--fails" in flags or "--by-spec" not in flags:
        for fl, title, r in out:
            if r == "failed":
                print(f"  FAIL  {os.path.basename(fl or '?')} :: {title[:70]}")
    print(f"\n{p} passed / {f} failed / {sk} skipped  ({len(out)} specs)")


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""Render an end-to-end suite's scenarios, results and source as one HTML page.

The page is the deliverable a reader opens: what was tested, what it did, and
the code that did it. Writing it by hand each time is a waste of a session and
drifts in style, so this script owns the layout and the palette and takes
everything specific to a run from a JSON manifest.

    python3 build_report.py manifest.json out.html

The manifest (every key optional unless marked):

    title            str   page <title> and gallery name: a short product-like name
    eyebrow          str   small line above the headline
    headline         str   large headline; defaults to title
    headline_tail    str   dimmed continuation of the headline
    lede             [str] one or two paragraphs under the headline
    figures          [{label, value, unit, note, tone}]  tone: pass|fail|plain
                           auto-filled with scenario counts when omitted
    run              [{caption, command}]  how to run the suite
    determinism      str   prose on how the suite avoids flakes
    suites           [{name, what, scenarios: [{name, doc, status, detail}]}]
                           status: PASS|FAIL|SKIP
    findings         [{title, status, body}]  status: open|fixed|note
    source_root      str   REQUIRED when groups is given: directory the paths are under
    groups           [{title, blurb, files: [{path, lang, purpose}]}]
    footer           str   left-hand footer line

Nothing here talks to the network: highlight.js is loaded from the one CDN the
Artifact CSP admits, and the page still reads as plain code if it never arrives.
"""

import html
import json
import pathlib
import sys

LANG_BY_SUFFIX = {
    ".go": "go", ".py": "python", ".sql": "sql", ".sh": "bash", ".md": "markdown",
    ".js": "javascript", ".ts": "typescript", ".rb": "ruby", ".rs": "rust",
    ".java": "java", ".json": "json", ".yaml": "yaml", ".yml": "yaml", ".mod": "go",
}

STATUS_TONE = {"PASS": "pass", "FAIL": "fail", "SKIP": "muted"}


def esc(text):
    return html.escape(str(text), quote=False)


def slug(path):
    return "f-" + "".join(c if c.isalnum() else "-" for c in str(path))


def read_source(root, path):
    return (pathlib.Path(root) / path).read_text(encoding="utf-8", errors="replace")


def counts(suites):
    total = passed = failed = skipped = 0
    for suite in suites:
        for scenario in suite.get("scenarios", []):
            total += 1
            status = scenario.get("status", "PASS").upper()
            passed += status == "PASS"
            failed += status == "FAIL"
            skipped += status == "SKIP"
    return total, passed, failed, skipped


def figures_block(manifest, suites, source_lines, source_files):
    figures = list(manifest.get("figures", []))
    if not figures:
        total, passed, failed, skipped = counts(suites)
        figures = [
            {"label": "Scenarios", "value": total},
            {"label": "Passing", "value": passed, "tone": "pass"},
            {"label": "Failing", "value": failed, "tone": "fail" if failed else "plain"},
        ]
        if skipped:
            figures.append({"label": "Skipped", "value": skipped})
        if source_files:
            figures.append({"label": "Source", "value": f"{source_lines:,}",
                            "note": f"lines · {source_files} files"})
    cells = []
    for figure in figures:
        tone = {"pass": " class=\"is-pass\"", "fail": " class=\"is-fail\""}.get(figure.get("tone"), "")
        unit = f"<small>{esc(figure['unit'])}</small>" if figure.get("unit") else ""
        note = f" <small>{esc(figure['note'])}</small>" if figure.get("note") else ""
        cells.append(f"<div><dt>{esc(figure['label'])}</dt>"
                     f"<dd{tone}>{esc(figure['value'])}{unit}{note}</dd></div>")
    return "".join(cells)


def suites_block(suites):
    if not suites:
        return ""
    rows = []
    for suite in suites:
        scenarios = suite.get("scenarios", [])
        total = len(scenarios)
        passed = sum(1 for s in scenarios if s.get("status", "PASS").upper() == "PASS")
        failed = sum(1 for s in scenarios if s.get("status", "PASS").upper() == "FAIL")
        rows.append(
            f'<tr class="suite-row"><td class="suite-name">{esc(suite["name"])}</td>'
            f'<td class="suite-what">{esc(suite.get("what", ""))}</td>'
            f'<td class="num">{total}</td><td class="num pass">{passed}</td>'
            f'<td class="num {"fail" if failed else "muted"}">{failed or "—"}</td></tr>')
        for scenario in scenarios:
            status = scenario.get("status", "PASS").upper()
            tone = STATUS_TONE.get(status, "muted")
            detail = (f'<span class="scenario-detail">{esc(scenario["detail"])}</span>'
                      if scenario.get("detail") else "")
            rows.append(
                f'<tr class="scenario-row"><td class="scenario-name">{esc(scenario["name"])}</td>'
                f'<td class="scenario-doc" colspan="3">{esc(scenario.get("doc", ""))}{detail}</td>'
                f'<td class="num"><span class="chip {tone}">{esc(status)}</span></td></tr>')
    return ('<section class="block"><h2>Scenarios, and what each one pins down</h2>'
            '<div class="table-wrap"><table><thead><tr><th>Suite / scenario</th>'
            '<th>What it covers</th><th class="num">Cases</th><th class="num">Pass</th>'
            '<th class="num">Fail</th></tr></thead><tbody>' + "".join(rows) +
            "</tbody></table></div></section>")


def findings_block(findings):
    if not findings:
        return ""
    items = []
    for finding in findings:
        status = finding.get("status", "note").lower()
        label = {"open": "Open", "fixed": "Fixed", "note": "Note"}.get(status, "Note")
        items.append(f'<li class="finding is-{esc(status)}">'
                     f'<span class="finding-flag">{esc(label)}</span>'
                     f'<div><h3>{esc(finding["title"])}</h3><p>{esc(finding.get("body", ""))}</p></div></li>')
    return ('<section class="block"><h2>What the run found</h2>'
            '<ul class="findings">' + "".join(items) + "</ul></section>")


def run_block(steps):
    if not steps:
        return ""
    rendered = []
    for index, step in enumerate(steps, start=1):
        rendered.append(
            f'<div class="step"><span class="step-n">{index:02d}</span><div class="step-body">'
            f'<p>{esc(step.get("caption", ""))}</p>'
            f'<div class="shell-line"><span class="prompt">$ </span>{esc(step["command"])}</div>'
            "</div></div>")
    return ('<section class="block"><h2>Running it</h2>'
            '<div class="run-steps">' + "".join(rendered) + "</div></section>")


def source_blocks(manifest):
    groups = manifest.get("groups", [])
    if not groups:
        return "", "", 0, 0
    root = manifest["source_root"]
    nav, body, total_lines, total_files = [], [], 0, 0

    for group in groups:
        nav.append(f'<li class="nav-group"><span class="nav-group-name">{esc(group["title"])}</span><ul>')
        body.append('<section class="group"><header class="group-head">'
                    f'<h2>{esc(group["title"])}</h2><p>{esc(group.get("blurb", ""))}</p></header>')
        for entry in group.get("files", []):
            path = entry["path"]
            source = read_source(root, path)
            lines = len(source.splitlines())
            total_lines += lines
            total_files += 1
            lang = entry.get("lang") or LANG_BY_SUFFIX.get(pathlib.Path(path).suffix, "plaintext")
            head, _, name = path.rpartition("/")
            nav.append(f'<li><a href="#{slug(path)}"><span class="nav-file">{esc(name or path)}</span>'
                       f'<span class="nav-lines">{lines}</span></a></li>')
            body.append(
                f'<details class="file" id="{slug(path)}" open><summary>'
                f'<span class="file-path"><span class="file-dir">{esc(head + "/" if head else "")}</span>'
                f'<span class="file-name">{esc(name or path)}</span></span>'
                f'<span class="file-meta"><span class="file-lines">{lines} lines</span>'
                '<span class="chev" aria-hidden="true"></span></span></summary>'
                f'<p class="file-purpose">{esc(entry.get("purpose", ""))}</p>'
                f'<div class="code-wrap"><pre><code class="language-{esc(lang)}">{esc(source)}</code></pre></div>'
                "</details>")
        nav.append("</ul></li>")
        body.append("</section>")

    return "".join(nav), "".join(body), total_lines, total_files


TEMPLATE = """<title>__TITLE__</title>
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500;600&family=IBM+Plex+Sans:wght@400;500;600;700&family=IBM+Plex+Serif:ital,wght@0,400;0,500;1,400&display=swap">
<style>
:root {
  --ground: #eaeeee; --surface: #ffffff; --surface-sunk: #f4f7f6;
  --ink: #0f1718; --ink-soft: #3a4749; --muted: #62716f;
  --rule: #d2dbd9; --rule-soft: #e2e9e7;
  --accent: #086468; --accent-soft: #0a7d81;
  --pass: #2a6b47; --fail: #a33c26;
  --pass-bg: #e2efe7; --fail-bg: #f6e4de;
  --code-key: #7a3aa0; --code-str: #1f6b4a; --code-com: #7b8785;
  --code-num: #9a5518; --code-typ: #0a6165; --code-fun: #1c4f9c;
  --shadow: 0 1px 2px rgba(15, 23, 24, .06), 0 8px 24px -18px rgba(15, 23, 24, .5);
  --sans: "IBM Plex Sans", ui-sans-serif, system-ui, sans-serif;
  --serif: "IBM Plex Serif", Georgia, serif;
  --mono: "IBM Plex Mono", ui-monospace, "SFMono-Regular", Menlo, monospace;
}
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) {
    --ground: #0b1011; --surface: #121a1b; --surface-sunk: #0e1516;
    --ink: #dde6e4; --ink-soft: #b3c0be; --muted: #82918f;
    --rule: #223030; --rule-soft: #1a2525;
    --accent: #57b8bb; --accent-soft: #7ccfd1;
    --pass: #63bd8d; --fail: #e2856a;
    --pass-bg: #163024; --fail-bg: #341f19;
    --code-key: #c79bea; --code-str: #7fc9a2; --code-com: #6e7d7c;
    --code-num: #e0a26a; --code-typ: #6fc7cb; --code-fun: #8ab4f0;
    --shadow: 0 1px 2px rgba(0, 0, 0, .5), 0 10px 30px -22px rgba(0, 0, 0, .9);
  }
}
:root[data-theme="dark"] {
  --ground: #0b1011; --surface: #121a1b; --surface-sunk: #0e1516;
  --ink: #dde6e4; --ink-soft: #b3c0be; --muted: #82918f;
  --rule: #223030; --rule-soft: #1a2525;
  --accent: #57b8bb; --accent-soft: #7ccfd1;
  --pass: #63bd8d; --fail: #e2856a;
  --pass-bg: #163024; --fail-bg: #341f19;
  --code-key: #c79bea; --code-str: #7fc9a2; --code-com: #6e7d7c;
  --code-num: #e0a26a; --code-typ: #6fc7cb; --code-fun: #8ab4f0;
  --shadow: 0 1px 2px rgba(0, 0, 0, .5), 0 10px 30px -22px rgba(0, 0, 0, .9);
}
* { box-sizing: border-box; }
body { margin: 0; background: var(--ground); color: var(--ink); font-family: var(--sans);
       font-size: 15px; line-height: 1.55; -webkit-font-smoothing: antialiased; }
a { color: var(--accent); }
:focus-visible { outline: 2px solid var(--accent-soft); outline-offset: 2px; border-radius: 2px; }
.shell { max-width: 1240px; margin: 0 auto; padding: 0 24px 96px; display: grid;
         grid-template-columns: 232px minmax(0, 1fr); gap: 44px; align-items: start; }
.shell.no-rail { grid-template-columns: minmax(0, 1fr); }
.masthead { grid-column: 1 / -1; padding: 52px 0 30px; border-bottom: 1px solid var(--rule); }
.eyebrow { font-family: var(--mono); font-size: 11.5px; letter-spacing: .14em;
           text-transform: uppercase; color: var(--muted); margin: 0 0 14px; }
.masthead h1 { font-size: clamp(30px, 4.4vw, 46px); line-height: 1.05; letter-spacing: -.022em;
               font-weight: 600; margin: 0 0 16px; text-wrap: balance; }
.masthead h1 .dim { color: var(--muted); font-weight: 400; }
.lede { font-family: var(--serif); font-size: 17.5px; line-height: 1.62; color: var(--ink-soft);
        max-width: 66ch; margin: 0; }
.lede + .lede { margin-top: 14px; }
.figures { grid-column: 1 / -1; display: flex; flex-wrap: wrap; border-bottom: 1px solid var(--rule);
           padding: 0; margin: 0; }
.figures div { padding: 18px 30px 18px 0; margin-right: 30px; border-right: 1px solid var(--rule-soft); }
.figures div:last-child { border-right: 0; }
.figures dt { font-family: var(--mono); font-size: 10.5px; letter-spacing: .13em;
              text-transform: uppercase; color: var(--muted); margin: 0 0 5px; }
.figures dd { margin: 0; font-family: var(--mono); font-size: 20px; font-weight: 500;
              font-variant-numeric: tabular-nums; letter-spacing: -.01em; }
.figures dd small { font-size: 12.5px; color: var(--muted); font-weight: 400; letter-spacing: 0; }
.figures dd.is-pass { color: var(--pass); }
.figures dd.is-fail { color: var(--fail); }
.rail { position: sticky; top: 20px; padding-top: 34px; }
.rail-head { font-family: var(--mono); font-size: 10.5px; letter-spacing: .13em;
             text-transform: uppercase; color: var(--muted); margin: 0 0 12px; }
.rail ul { list-style: none; margin: 0; padding: 0; }
.nav-group { margin-bottom: 16px; }
.nav-group-name { display: block; font-size: 12px; font-weight: 600; color: var(--ink-soft);
                  padding-bottom: 5px; border-bottom: 1px solid var(--rule-soft); margin-bottom: 5px; }
.rail a { display: flex; justify-content: space-between; gap: 10px; padding: 3px 0;
          font-family: var(--mono); font-size: 12px; color: var(--muted); text-decoration: none; }
.rail a:hover { color: var(--accent); }
.nav-lines { font-variant-numeric: tabular-nums; opacity: .65; }
.main { padding-top: 34px; display: flex; flex-direction: column; gap: 44px; min-width: 0; }
.block h2 { font-size: 13px; font-family: var(--mono); font-weight: 500; letter-spacing: .12em;
            text-transform: uppercase; color: var(--muted); margin: 0 0 16px; }
.block p { font-family: var(--serif); color: var(--ink-soft); max-width: 66ch; margin: 0 0 12px; }
.table-wrap { overflow-x: auto; }
table { width: 100%; border-collapse: collapse; font-size: 14px; }
thead th { text-align: left; font-family: var(--mono); font-size: 10.5px; letter-spacing: .12em;
           text-transform: uppercase; color: var(--muted); font-weight: 500;
           padding: 0 12px 8px 0; border-bottom: 1px solid var(--rule); }
tbody td { padding: 9px 12px 9px 0; border-bottom: 1px solid var(--rule-soft); vertical-align: baseline; }
.suite-row td { padding-top: 16px; }
.suite-name { font-family: var(--mono); font-size: 12.5px; color: var(--ink); white-space: nowrap; font-weight: 500; }
.suite-what { color: var(--muted); }
.scenario-row td { border-bottom-color: var(--rule-soft); }
.scenario-name { font-family: var(--mono); font-size: 12px; color: var(--ink-soft); padding-left: 14px;
                 white-space: nowrap; }
.scenario-doc { color: var(--muted); font-size: 13.5px; }
.scenario-detail { display: block; font-family: var(--mono); font-size: 11.5px; color: var(--fail);
                   margin-top: 4px; overflow-wrap: anywhere; }
td.num, th.num { text-align: right; font-family: var(--mono); font-variant-numeric: tabular-nums;
                 padding-right: 0; width: 74px; }
td.pass { color: var(--pass); } td.fail { color: var(--fail); } td.muted { color: var(--muted); }
.chip { display: inline-block; font-family: var(--mono); font-size: 10.5px; letter-spacing: .08em;
        padding: 2px 7px; border-radius: 3px; }
.chip.pass { color: var(--pass); background: var(--pass-bg); }
.chip.fail { color: var(--fail); background: var(--fail-bg); }
.chip.muted { color: var(--muted); background: var(--surface-sunk); }
.findings { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 14px; }
.finding { display: grid; grid-template-columns: 62px minmax(0, 1fr); gap: 16px; align-items: start;
           background: var(--surface); border: 1px solid var(--rule); border-radius: 6px; padding: 14px 16px; }
.finding-flag { font-family: var(--mono); font-size: 10px; letter-spacing: .1em; text-transform: uppercase;
                padding: 3px 0; color: var(--muted); }
.finding.is-open .finding-flag { color: var(--fail); }
.finding.is-fixed .finding-flag { color: var(--pass); }
.finding h3 { margin: 0 0 5px; font-size: 15px; font-weight: 600; letter-spacing: -.005em; }
.finding p { margin: 0; font-family: var(--serif); color: var(--ink-soft); font-size: 14.5px; max-width: 70ch; }
.run-steps { display: flex; flex-direction: column; gap: 14px; }
.step { display: grid; grid-template-columns: 26px minmax(0, 1fr); gap: 14px; align-items: start; }
.step-n { font-family: var(--mono); font-size: 11px; color: var(--muted); padding-top: 9px;
          font-variant-numeric: tabular-nums; }
.step-body p { margin: 0 0 8px; font-size: 14.5px; }
.shell-line { font-family: var(--mono); font-size: 12.5px; background: var(--surface);
              border: 1px solid var(--rule); border-radius: 5px; padding: 11px 14px;
              overflow-x: auto; white-space: pre; color: var(--ink); }
.shell-line .prompt { color: var(--muted); user-select: none; }
.note { border-left: 2px solid var(--accent); padding: 2px 0 2px 16px; font-family: var(--serif);
        color: var(--ink-soft); max-width: 66ch; }
.source-head { display: flex; align-items: baseline; justify-content: space-between; gap: 20px;
               flex-wrap: wrap; border-bottom: 1px solid var(--rule); padding-bottom: 12px; }
.controls { display: flex; gap: 8px; }
.controls button { font-family: var(--mono); font-size: 11px; letter-spacing: .06em; text-transform: uppercase;
                   color: var(--muted); background: transparent; border: 1px solid var(--rule);
                   border-radius: 4px; padding: 5px 10px; cursor: pointer; }
.controls button:hover { color: var(--accent); border-color: var(--accent); }
.group { display: flex; flex-direction: column; gap: 14px; }
.group-head { margin-top: 14px; }
.group-head h2 { font-size: 19px; font-weight: 600; letter-spacing: -.01em; margin: 0 0 3px;
                 font-family: var(--sans); text-transform: none; letter-spacing: -.01em; color: var(--ink); }
.group-head p { font-family: var(--serif); color: var(--muted); margin: 0; font-size: 14.5px; }
.file { background: var(--surface); border: 1px solid var(--rule); border-radius: 6px;
        box-shadow: var(--shadow); scroll-margin-top: 16px; overflow: hidden; }
.file summary { display: flex; align-items: center; justify-content: space-between; gap: 16px;
                padding: 11px 16px; cursor: pointer; list-style: none; }
.file summary::-webkit-details-marker { display: none; }
.file-path { font-family: var(--mono); font-size: 13px; min-width: 0; overflow-wrap: anywhere; }
.file-dir { color: var(--muted); }
.file-name { color: var(--ink); font-weight: 500; }
.file-meta { display: flex; align-items: center; gap: 12px; flex: none; }
.file-lines { font-family: var(--mono); font-size: 11.5px; color: var(--muted); font-variant-numeric: tabular-nums; }
.chev { width: 7px; height: 7px; border-right: 1.5px solid var(--muted); border-bottom: 1.5px solid var(--muted);
        transform: rotate(45deg); transition: transform .15s ease; margin-bottom: 3px; }
.file[open] .chev { transform: rotate(-135deg); margin-bottom: -3px; }
.file-purpose { margin: 0; padding: 0 16px 12px; font-family: var(--serif); font-size: 14px;
                color: var(--muted); max-width: 72ch; }
.code-wrap { border-top: 1px solid var(--rule-soft); background: var(--surface-sunk); overflow-x: auto; }
pre { margin: 0; padding: 16px; }
code { font-family: var(--mono); font-size: 12.5px; line-height: 1.5; color: var(--ink-soft);
       white-space: pre; tab-size: 4; }
.hljs-keyword, .hljs-literal, .hljs-meta { color: var(--code-key); }
.hljs-string, .hljs-regexp, .hljs-symbol { color: var(--code-str); }
.hljs-comment, .hljs-quote { color: var(--code-com); }
.hljs-number { color: var(--code-num); }
.hljs-type, .hljs-built_in, .hljs-class .hljs-title { color: var(--code-typ); }
.hljs-title, .hljs-function .hljs-title, .hljs-attr, .hljs-section { color: var(--code-fun); }
.hljs-name, .hljs-tag { color: var(--code-typ); }
.hljs-strong { font-weight: 600; }
footer { grid-column: 1 / -1; margin-top: 56px; padding-top: 20px; border-top: 1px solid var(--rule);
         font-family: var(--mono); font-size: 11.5px; color: var(--muted); display: flex;
         justify-content: space-between; gap: 16px; flex-wrap: wrap; }
@media (max-width: 900px) {
  .shell { grid-template-columns: minmax(0, 1fr); gap: 0; padding: 0 18px 64px; }
  .rail { position: static; padding-top: 28px; border-bottom: 1px solid var(--rule); padding-bottom: 20px; }
  .rail ul.index { columns: 2; column-gap: 24px; }
  .figures div { padding-right: 22px; margin-right: 22px; }
  .finding { grid-template-columns: minmax(0, 1fr); gap: 6px; }
}
@media (prefers-reduced-motion: reduce) { * { transition: none !important; animation: none !important; } }
</style>

<div class="shell__RAILCLASS__">
  <header class="masthead">
    __EYEBROW__
    <h1>__HEADLINE__</h1>
    __LEDE__
  </header>

  <dl class="figures">__FIGURES__</dl>

  __RAIL__

  <main class="main">
    __SUITES__
    __FINDINGS__
    __RUN__
    __DETERMINISM__
    __SOURCE_HEAD__
    __SOURCE__
  </main>

  <footer><span>__FOOTER__</span><span>__FOOTER_RIGHT__</span></footer>
</div>

<script src="https://cdnjs.cloudflare.com/ajax/libs/highlight.js/11.9.0/highlight.min.js"></script>
<script>
  if (window.hljs) {
    document.querySelectorAll('pre code').forEach(function (block) {
      try { window.hljs.highlightElement(block); } catch (e) { /* plain text is a fine fallback */ }
    });
  }
  document.querySelectorAll('.controls button').forEach(function (button) {
    button.addEventListener('click', function () {
      var open = button.dataset.all === 'open';
      document.querySelectorAll('details.file').forEach(function (file) { file.open = open; });
    });
  });
</script>
"""


def build(manifest):
    suites = manifest.get("suites", [])
    nav, source, source_lines, source_files = source_blocks(manifest)

    headline = esc(manifest.get("headline", manifest["title"]))
    if manifest.get("headline_tail"):
        headline += f' <span class="dim">{esc(manifest["headline_tail"])}</span>'

    determinism = manifest.get("determinism", "")
    source_head = ('<section class="block"><div class="source-head"><h2 style="margin:0">Source</h2>'
                   '<div class="controls"><button type="button" data-all="open">Expand all</button>'
                   '<button type="button" data-all="close">Collapse all</button></div></div></section>'
                   ) if source else ""

    replacements = {
        "__TITLE__": esc(manifest["title"]),
        "__RAILCLASS__": "" if nav else " no-rail",
        "__EYEBROW__": f'<p class="eyebrow">{esc(manifest["eyebrow"])}</p>' if manifest.get("eyebrow") else "",
        "__HEADLINE__": headline,
        "__LEDE__": "".join(f'<p class="lede">{esc(p)}</p>' for p in manifest.get("lede", [])),
        "__FIGURES__": figures_block(manifest, suites, source_lines, source_files),
        "__RAIL__": (f'<nav class="rail" aria-label="Source files"><p class="rail-head">Contents</p>'
                     f'<ul class="index">{nav}</ul></nav>') if nav else "",
        "__SUITES__": suites_block(suites),
        "__FINDINGS__": findings_block(manifest.get("findings", [])),
        "__RUN__": run_block(manifest.get("run", [])),
        "__DETERMINISM__": (f'<section class="block"><h2>How a scenario stays deterministic</h2>'
                            f'<p class="note">{esc(determinism)}</p></section>') if determinism else "",
        "__SOURCE_HEAD__": source_head,
        "__SOURCE__": source,
        "__FOOTER__": esc(manifest.get("footer", manifest["title"])),
        "__FOOTER_RIGHT__": (f"{source_lines:,} lines across {source_files} files" if source_files else ""),
    }

    page = TEMPLATE
    for token, value in replacements.items():
        page = page.replace(token, value)
    return page


def main():
    if len(sys.argv) != 3:
        print(__doc__)
        return 2
    manifest = json.loads(pathlib.Path(sys.argv[1]).read_text())
    output = pathlib.Path(sys.argv[2])
    output.write_text(build(manifest))
    total, passed, failed, skipped = counts(manifest.get("suites", []))
    print(f"wrote {output} ({output.stat().st_size / 1024:.0f} KB) — "
          f"{total} scenarios, {passed} passed, {failed} failed, {skipped} skipped")
    return 0


if __name__ == "__main__":
    sys.exit(main())

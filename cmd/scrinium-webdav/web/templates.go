package web

import "html/template"

// listingTemplate is the embedded HTML template for directory
// pages. Plain HTML + minimal CSS (legibility-only, no
// JavaScript). System fonts so we don't depend on a network-
// loaded font.
//
// Phases 2 and 3 will introduce sibling templates
// (artifactTemplate, statsTemplate); they share the same
// header/footer aesthetic but live as separate templates so
// edits to one don't risk breaking the others.
var listingTemplate = template.Must(template.New("listing").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Path}} — Scrinium</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif;
         background: #fafafa; color: #222; margin: 0; padding: 1.5em 2em; }
  header { display: flex; align-items: baseline; gap: 1em;
           border-bottom: 1px solid #e0e0e0; padding-bottom: 0.7em; margin-bottom: 1em; }
  header .brand { font-weight: 600; color: #06f; font-size: 1.1em; letter-spacing: 0.02em; }
  header .store { color: #888; font-size: 0.9em;
                  font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace; }
  .crumbs { font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace;
            font-size: 0.95em; margin-bottom: 1.2em; }
  .crumbs a { color: #06f; text-decoration: none; }
  .crumbs a:hover { text-decoration: underline; }
  .crumbs .sep { color: #aaa; margin: 0 0.3em; }
  table { border-collapse: collapse; width: 100%; max-width: 1100px;
          table-layout: fixed; }
  th, td { padding: 0.4em 1em; text-align: left; }
  th { font-weight: 500; color: #888; font-size: 0.9em;
       border-bottom: 1px solid #ddd; }
  th a { color: inherit; text-decoration: none; }
  th a:hover { color: #06f; }
  th.size, th.time { text-align: left; }
  th.size { width: 9em; }
  th.time { width: 11em; }
  th.actions { width: 6em; }
  tr:nth-child(even) td { background: #f3f3f3; }
  td.name { font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace;
            overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
            max-width: 0; /* makes table-layout:fixed honour the cell width */ }
  td.name a { color: #06f; text-decoration: none; }
  td.name a:hover { text-decoration: underline; }
  td.size, td.time { color: #666; font-variant-numeric: tabular-nums;
                     font-family: ui-monospace, monospace; font-size: 0.92em;
                     white-space: nowrap; }
  td.size { text-align: right; }
  td.actions { width: 6em; text-align: right; white-space: nowrap; }
  td.actions a { color: #888; text-decoration: none; font-size: 0.85em;
                 padding: 0.1em 0.5em; border-radius: 3px;
                 margin-left: 0.2em; }
  td.actions a:hover { color: #06f; background: #ececec; }
  .icon-dir  { color: #999; margin-right: 0.5em; }
  .icon-file { color: #ccc; margin-right: 0.5em; }
  /* System entries (paths beginning with "_") are dimmed and
     tagged so they're visually subordinate to user content
     while still discoverable. */
  tr.system td.name a { color: #888; }
  tr.system td.name a:hover { color: #06f; }
  tr.system .icon-dir, tr.system .icon-file { color: #c0c0c0; }
  tr.system .badge { display: inline-block; margin-left: 0.6em; padding: 0 0.4em;
                     font-size: 0.72em; line-height: 1.4; border-radius: 3px;
                     background: #ececec; color: #999; vertical-align: 0.05em;
                     letter-spacing: 0.03em; text-transform: lowercase; }
  footer { margin-top: 3em; padding-top: 0.8em; border-top: 1px solid #e0e0e0;
           color: #888; font-size: 0.85em; }
  footer a { color: #06f; text-decoration: none; }
  footer a:hover { text-decoration: underline; }
  .summary { margin-top: 0.6em; color: #888; font-size: 0.85em;
             font-variant-numeric: tabular-nums; }
  .pagination { margin-top: 0.6em; font-size: 0.9em; display: flex;
                gap: 1em; align-items: baseline; }
  .pagination .pages { color: #888; }
  .pagination a { color: #06f; text-decoration: none; }
  .pagination a:hover { text-decoration: underline; }
  .pagination .disabled { color: #ccc; }
</style>
</head>
<body>

<header>
  <span class="brand">Scrinium</span>
  <span class="store">{{.StorePath}}</span>
</header>

<div class="crumbs">
  {{range $i, $c := .Crumbs}}{{if $i}}<span class="sep">/</span>{{end}}<a href="{{$c.URL}}">{{$c.Name}}</a>{{end}}
</div>

<table>
  <thead>
    <tr>
      <th><a href="{{.SortNameURL}}">Name{{.SortNameArrow}}</a></th>
      <th class="size"><a href="{{.SortSizeURL}}">Size{{.SortSizeArrow}}</a></th>
      <th class="time"><a href="{{.SortTimeURL}}">Modified{{.SortTimeArrow}}</a></th>
      <th class="actions"></th>
    </tr>
  </thead>
  <tbody>
{{- if .HasParent}}
    <tr>
      <td class="name"><span class="icon-dir">↑</span><a href="{{.Parent}}">..</a></td>
      <td class="size">—</td>
      <td class="time"></td>
      <td class="actions"></td>
    </tr>
{{- end}}
{{- range .Entries}}
    <tr{{if .IsSystem}} class="system"{{end}}>
      <td class="name">{{if .IsDir}}<span class="icon-dir">▸</span>{{else}}<span class="icon-file">·</span>{{end}}{{if .URL}}<a href="{{.URL}}" title="{{.Name}}">{{.Name}}{{if .IsDir}}/{{end}}</a>{{else}}<span title="{{.Name}}">{{.Name}}</span>{{end}}{{if .IsSystem}}<span class="badge">system</span>{{end}}</td>
      <td class="size">{{.SizeText}}</td>
      <td class="time">{{.TimeText}}</td>
      <td class="actions">{{if .ViewURL}}<a href="{{.ViewURL}}" target="_blank" rel="noopener">view</a>{{end}}{{if .DownloadURL}}<a href="{{.DownloadURL}}">dl</a>{{end}}</td>
    </tr>
{{- end}}
  </tbody>
</table>

<div class="summary">
  {{.TotalDirs}} {{if eq .TotalDirs 1}}directory{{else}}directories{{end}},
  {{.TotalFiles}} {{if eq .TotalFiles 1}}file{{else}}files{{end}}{{if gt .TotalFiles 0}}, {{.TotalBytesFmt}}{{end}}
</div>

{{if gt .TotalPages 1}}
<div class="pagination">
  <span class="pages">page {{.Page}} of {{.TotalPages}}</span>
  {{if .PrevURL}}<a href="{{.PrevURL}}">← prev</a>{{else}}<span class="disabled">← prev</span>{{end}}
  {{if .NextURL}}<a href="{{.NextURL}}">next →</a>{{else}}<span class="disabled">next →</span>{{end}}
</div>
{{end}}

<footer>
  {{.NowFormatted}} · <a href="{{.BrowsePrefix}}/_stats">stats</a> · <a href="{{.StatsURL}}">text stats</a>
</footer>

</body>
</html>
`))

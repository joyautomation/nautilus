package checkout

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"time"
)

// Report is a run as a printable page: the sheet's points with every result,
// who recorded, and (for a sealed run) who signed off, under which rule, with
// the controller's evidence and the key's fingerprint. It is built from the
// export, so a sealed run's report shows the signed bytes' content, and html/
// template escapes every free-text field (a note is a person's words). Nothing
// external is loaded. It says plainly what the record is and is not.
type Report struct {
	Export Export
	// Seal is the payload of the run's sealed record, nil for a run not sealed.
	Seal *SealPayload
	// CurrentKey is the fingerprint of the controller's signing key now: a
	// sealed record signed by another key (the key file was replaced, or the
	// record was) is shown as such, not as this controller's.
	CurrentKey string
}

// ReportFromSealed builds the report of a sealed record: the export the
// payload hashes, and the payload. The caller has verified it (Verify).
func ReportFromSealed(record []byte) (Report, error) {
	var sr SealedRecord
	if err := json.Unmarshal(record, &sr); err != nil {
		return Report{}, err
	}
	var ex Export
	if err := json.Unmarshal(sr.Export, &ex); err != nil {
		return Report{}, err
	}
	var pl SealPayload
	if err := json.Unmarshal(sr.Payload, &pl); err != nil {
		return Report{}, err
	}
	return Report{Export: ex, Seal: &pl}, nil
}

var reportTmpl = template.Must(template.New("report").Funcs(template.FuncMap{
	"when": func(ms int64) string {
		if ms == 0 {
			return ""
		}
		return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04:05 UTC")
	},
	"val": func(v any) string {
		switch x := v.(type) {
		case nil:
			return ""
		case string:
			return x
		default:
			b, _ := json.Marshal(x)
			return string(b)
		}
	},
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<title>I/O checkout {{.Export.Run.ID}}</title>
<style>
body{font:14px/1.4 system-ui,sans-serif;margin:24px;color:#111}
h1{font-size:20px;margin:0 0 4px} h2{font-size:15px;margin:22px 0 6px}
.banner{padding:8px 12px;border:2px solid #111;font-weight:700;margin:10px 0}
.draft{border-color:#b45309;background:#fef3c7} .sealed{border-color:#166534;background:#dcfce7}
.notice{font-size:12px;color:#333;border-left:3px solid #999;padding-left:10px;margin:10px 0}
table{border-collapse:collapse;width:100%;margin:6px 0} th,td{border:1px solid #999;padding:3px 6px;text-align:left;vertical-align:top;font-size:12px}
th{background:#eee} .fail{color:#b91c1c;font-weight:700} .mono{font-family:ui-monospace,monospace;word-break:break-all}
@media print{body{margin:10mm} tr{page-break-inside:avoid}}
</style></head><body>
<h1>{{if .Export.Title}}{{.Export.Title}}{{else}}I/O checkout{{end}}</h1>
<div>Run <span class="mono">{{.Export.Run.ID}}</span>, begun {{when .Export.Run.At}} by {{.Export.Run.By}}{{if .Export.Rehearsal}} (rehearsal){{end}}</div>
{{if .Seal}}<div class="banner sealed">SEALED: signed off by {{.Seal.Signer}} on {{when .Seal.SealedAt}} ({{.Seal.Mode}})</div>
{{if ne .Seal.KeyFingerprint .CurrentKey}}<div class="banner draft">This record is signed by key {{.Seal.KeyFingerprint}}, which is not this controller's current signing key{{if .CurrentKey}} ({{.CurrentKey}}){{end}}: it is the controller's only if you recorded that fingerprint from it.</div>{{end}}
{{else}}<div class="banner draft">DRAFT, NOT SIGNED OFF: {{.Export.Status}}</div>{{end}}
<div class="notice">{{.Notice}}</div>
{{if .Seal}}
<h2>Sign-off</h2>
<table>
<tr><th>Signed off by</th><td>{{.Seal.Signer}} ({{.Seal.SignerPrincipal}})</td></tr>
<tr><th>Rule</th><td>{{.Seal.Mode}}{{if .Seal.Statement}}: {{.Seal.Statement}}{{end}}</td></tr>
<tr><th>Recorded results</th><td>{{range $i, $r := .Seal.Recorders}}{{if $i}}, {{end}}{{$r}}{{end}}</td></tr>
{{if .Seal.Note}}<tr><th>Note</th><td>{{.Seal.Note}}</td></tr>{{end}}
<tr><th>Signing key</th><td class="mono">{{.Seal.KeyFingerprint}}</td></tr>
<tr><th>Export hash (SHA-256)</th><td class="mono">{{.Seal.ExportSha256}}</td></tr>
<tr><th>Place in the results file</th><td>after entry {{.Seal.PrevSeq}}</td></tr>
</table>
<h2>What the controller was connected to when it signed</h2>
<table><tr><th>Driver</th><th>Kind</th></tr>
{{range .Seal.Evidence.Drivers}}<tr><td>{{.Name}}</td><td>{{.Kind}}</td></tr>{{else}}<tr><td colspan="2">none recorded</td></tr>{{end}}</table>
<table><tr><th>Device</th><th>Endpoint</th><th>Class</th></tr>
{{range .Seal.Evidence.Devices}}<tr><td>{{.Name}}</td><td>{{.Endpoint}}</td><td>{{.Class}}</td></tr>{{else}}<tr><td colspan="3">none recorded</td></tr>{{end}}</table>
<p>Check this record with <span class="mono">naut checkout verify</span> against the controller's results file, and compare the key's fingerprint with one written down from the controller.</p>
{{end}}
<h2>Points</h2>
<table><tr><th>Point</th><th>Tag</th><th>Kind</th><th>Where</th><th>Expect</th><th>Result</th></tr>
{{range .Export.Points}}<tr>
<td>{{.ID}}</td><td>{{.Tag}}</td><td>{{.Kind}}</td><td>{{.Device}} {{.Address}} {{.Area}}</td><td>{{.Expect}}</td>
<td>{{if not .Results}}not checked{{end}}{{range .Results}}<div{{if eq .Outcome "fail"}} class="fail"{{end}}>{{.Outcome}}: {{.By}}, {{when .At}}{{if ne (val .Seen) ""}}, read {{val .Seen}}{{if .Quality}} ({{.Quality}}){{end}}{{end}}{{if .Force}}, forced {{val .Force.Value}} for {{.Force.Seconds}} s{{if .Force.Ended}} ({{.Force.Ended}}){{end}}{{end}}{{if .Note}}: {{.Note}}{{end}}</div>{{end}}</td>
</tr>{{end}}</table>
{{if .Export.Orphans}}<h2>Results for points the sheet no longer lists</h2>
<table><tr><th>Point</th><th>Result</th></tr>{{range .Export.Orphans}}<tr><td>{{.Point}}</td><td>{{.Outcome}}: {{.By}}, {{when .At}}{{if .Note}}: {{.Note}}{{end}}</td></tr>{{end}}</table>{{end}}
{{if .Export.SheetChanged}}<p class="fail">The sheet changed since this run began: the points are described by the current sheet.</p>{{end}}
</body></html>
`))

type reportData struct {
	Report
	Notice string
}

// HTML renders the report.
func (r Report) HTML() ([]byte, error) {
	var b bytes.Buffer
	if err := reportTmpl.Execute(&b, reportData{Report: r, Notice: SealNotice}); err != nil {
		return nil, fmt.Errorf("checkout: report: %w", err)
	}
	return b.Bytes(), nil
}

{{ define "intersects" }}
    {{- if .Intersects -}}
        Intersects: {{range $index, $item := .Intersects}}{{if $index}}, {{end}}{{.Name}}({{.Percent}}%){{end}}
    {{- end -}}
{{ end -}}
{{ define "issues" }}
    {{- range .Issues}}
[{{ .FilePath }}:{{ .Line }}]({{ getLink . }}){{ if .Pos.Column }}:{{ .Pos.Column }}{{ end }}: {{ .Text }}
```
{{ formatText . }}
```
    {{end}}
{{ end -}}

There are {{ .TotalIssuesCount }} issues found in {{ .ModulePath }}.
{{ range .SectionsOrder }}
# {{ . | title }} Linters
    {{- range index $.Sections .}}
## {{ .Name }}: {{ .Issues | len }} issues
{{ template "intersects" .}}
{{ if not .SubLinters }}{{ template "issues" .}}{{ end }}
        {{- range .SubLinters }}
### {{ .FullName }}: {{ .Issues | len }} issues
{{ template "intersects" .}}
{{ template "issues" .}}
        {{- end }}
    {{- end }}
{{ end }}

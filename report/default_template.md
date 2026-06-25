---
title: {{.Metadata.Title}}
client: {{.Metadata.Client}}
type: {{.Metadata.Type}}
assessor: {{.Metadata.Assessor}}
scope: {{.Metadata.Scope}}
draft: {{.Metadata.IsDraft}}
start: {{.Metadata.Start.Format "2006-01-02"}}
end: {{.Metadata.End.Format "2006-01-02"}}
created: {{.Metadata.CreatedAt.Local.Format "2006-01-02 15:04:05"}}
---

# Executive Summary

## Findings Breakdown

{{- $severityCounts := severityCount .Findings }}

| Severity | Count |
|-|-:|
| Critical | {{ index $severityCounts "Critical" }} |
| High | {{ index $severityCounts "High" }} |
| Medium | {{ index $severityCounts "Medium" }} |
| Low | {{ index $severityCounts "Low" }} |
| Informational | {{ index $severityCounts "Informational" }} |

## List of Findings

| Finding | Severity | Title |
|-|:-:|-|
{{- range $index, $finding := .Findings | sortFindings }}
| F-{{ printf "%02d" (inc $index) }} | {{ $finding.Severity }} | {{ $finding.Title }} |
{{- end }} {{/* findings summary range */}}

# Findings

{{- range $index, $finding := .Findings | sortFindings }}

## F-{{ printf "%02d" (inc $index) }} {{ $finding.Title }} {{ $finding.Severity }}

| {{ printf "%.1f" $finding.CVSSScore }} | {{ $finding.CVSSVector }} |
|-:|-|

### Description

{{ $finding.WriteUp }}

### Treatment Plan

{{ $finding.TreatmentPlan }}

{{- if $finding.Requests }}

### Requests

{{- range $reqIndex, $row := ($finding.Requests | getRows) }}

#### Request {{ inc $reqIndex }}

##### Request

```http
{{ truncate $row.Request.Raw $.Metadata.TruncateLength | cleanPrint }}
```

##### Response

```http
{{ truncate $row.Response.Raw $.Metadata.TruncateLength | cleanPrint }}
```

{{- end }} {{/* requests range */}}
{{- end }} {{/* requests if */}}

{{- if $finding.Artifacts }}

### Artifacts

{{- range $artIndex, $artifact := $finding.Artifacts }}

#### Artifact {{ inc $artIndex }}

{{- if isImage $artifact }}
<img src="{{ artifactDataURI $artifact }}" alt="{{ $artifact.Filename }}" style="width: 100%; max-width: 600px;" />
{{- else }}
{{ $artifact.Filename }}
{{- end }} {{/* isImage if */}}
{{- end }} {{/* artifacts range */}}
{{- end }} {{/* artifacts if */}}
{{- end }} {{/* findings range */}}

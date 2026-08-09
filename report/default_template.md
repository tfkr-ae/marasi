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

{{- if and (eq (index $row.Metadata "protocol") "websocket") (eq $row.Response.StatusCode 101) }}
{{- $webSocket := getWebSocket $row.Request.ID }}

##### WebSocket Connection

| Property | Value |
|-|-|
| Endpoint | {{ $webSocket.Connection.Transport }}://{{ $webSocket.Connection.Host }}{{ $webSocket.Connection.Path }} |
| State | {{ $webSocket.Connection.State }} |
| Started | {{ $webSocket.Connection.StartedAt.Local.Format "2006-01-02 15:04:05.000" }} |
{{- if $webSocket.Connection.ClosedAt }}
| Closed | {{ $webSocket.Connection.ClosedAt.Local.Format "2006-01-02 15:04:05.000" }} |
{{- end }}
{{- if $webSocket.Connection.CloseCode }}
| Close | {{ $webSocket.Connection.CloseCode }} {{ $webSocket.Connection.CloseReason }} |
{{- end }}

WebSocket messages are included in [Appendix A: WebSocket Messages](#appendix-finding-{{ inc $index }}-request-{{ inc $reqIndex }}).
{{- end }} {{/* websocket if */}}

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

{{- if .TestCases }}

# Test Cases

{{- range $testCaseIndex, $testCase := .TestCases }}

## TC-{{ printf "%02d" (inc $testCaseIndex) }} {{ $testCase.Title }}

| Category | Tags |
|-|-|
| {{ $testCase.Category }} | {{ join $testCase.Tags ", " }} |

### Description

{{ $testCase.Description }}

{{- if $testCase.Note }}

### Notes

{{ $testCase.Note }}
{{- end }}

{{- if $testCase.Requests }}

### Requests

{{- range $reqIndex, $row := ($testCase.Requests | getRows) }}

#### Request {{ inc $reqIndex }}

##### Request

```http
{{ truncate $row.Request.Raw $.Metadata.TruncateLength | cleanPrint }}
```

##### Response

```http
{{ truncate $row.Response.Raw $.Metadata.TruncateLength | cleanPrint }}
```

{{- if and (eq (index $row.Metadata "protocol") "websocket") (eq $row.Response.StatusCode 101) }}
{{- $webSocket := getWebSocket $row.Request.ID }}

##### WebSocket Connection

| Property | Value |
|-|-|
| Endpoint | {{ $webSocket.Connection.Transport }}://{{ $webSocket.Connection.Host }}{{ $webSocket.Connection.Path }} |
| State | {{ $webSocket.Connection.State }} |
| Started | {{ $webSocket.Connection.StartedAt.Local.Format "2006-01-02 15:04:05.000" }} |
{{- if $webSocket.Connection.ClosedAt }}
| Closed | {{ $webSocket.Connection.ClosedAt.Local.Format "2006-01-02 15:04:05.000" }} |
{{- end }}
{{- if $webSocket.Connection.CloseCode }}
| Close | {{ $webSocket.Connection.CloseCode }} {{ $webSocket.Connection.CloseReason }} |
{{- end }}

WebSocket messages are included in [Appendix A: WebSocket Messages](#appendix-test-case-{{ inc $testCaseIndex }}-request-{{ inc $reqIndex }}).
{{- end }} {{/* test case websocket if */}}

{{- end }} {{/* test case requests range */}}
{{- end }} {{/* test case requests if */}}

{{- if $testCase.Artifacts }}

### Artifacts

{{- range $artIndex, $artifact := $testCase.Artifacts }}

#### Artifact {{ inc $artIndex }}

{{- if isImage $artifact }}
<img src="{{ artifactDataURI $artifact }}" alt="{{ $artifact.Filename }}" style="width: 100%; max-width: 600px;" />
{{- else }}
{{ $artifact.Filename }}
{{- end }} {{/* isImage if */}}
{{- end }} {{/* test case artifacts range */}}
{{- end }} {{/* test case artifacts if */}}
{{- end }} {{/* test cases range */}}
{{- end }} {{/* test cases if */}}

{{- $appendixStarted := false }}
{{- range $findingIndex, $finding := .Findings }}
{{- range $requestIndex, $row := ($finding.Requests | getRows) }}
{{- if and (eq (index $row.Metadata "protocol") "websocket") (eq $row.Response.StatusCode 101) }}
{{- if not $appendixStarted }}

# Appendix A: WebSocket Messages
{{- $appendixStarted = true }}
{{- end }}
{{- $webSocket := getWebSocket $row.Request.ID }}

<a id="appendix-finding-{{ inc $findingIndex }}-request-{{ inc $requestIndex }}"></a>

## Finding {{ inc $findingIndex }} - Request {{ inc $requestIndex }}

{{- range $messageIndex, $message := $webSocket.Messages }}
{{- $dropped := index $message.Metadata "dropped" }}

### {{ if $dropped }}~~Message {{ inc $messageIndex }}~~{{ else }}Message {{ inc $messageIndex }}{{ end }}

| Time | Direction | Opcode | Binary | Status |
|-|-|-:|:-:|-|
| {{ $message.CreatedAt.Local.Format "2006-01-02 15:04:05.000" }} | {{ $message.Direction }} | {{ $message.Opcode }} | {{ $message.IsBinary }} | {{ if $dropped }}dropped{{ if index $message.Metadata "injected" }}, {{ end }}{{ end }}{{ if index $message.Metadata "injected" }}injected{{ end }} |

```text
{{ truncate $message.Payload $.Metadata.TruncateLength | cleanPrint }}
```

{{- if $message.Metadata }}

#### Metadata

```json
{{ toJSON $message.Metadata }}
```
{{- end }}
{{- else }}

No WebSocket messages were captured.
{{- end }} {{/* finding websocket messages range */}}
{{- end }} {{/* finding websocket if */}}
{{- end }} {{/* finding requests range */}}
{{- end }} {{/* appendix findings range */}}

{{- range $testCaseIndex, $testCase := .TestCases }}
{{- range $requestIndex, $row := ($testCase.Requests | getRows) }}
{{- if and (eq (index $row.Metadata "protocol") "websocket") (eq $row.Response.StatusCode 101) }}
{{- if not $appendixStarted }}

# Appendix A: WebSocket Messages
{{- $appendixStarted = true }}
{{- end }}
{{- $webSocket := getWebSocket $row.Request.ID }}

<a id="appendix-test-case-{{ inc $testCaseIndex }}-request-{{ inc $requestIndex }}"></a>

## Test Case {{ inc $testCaseIndex }} - Request {{ inc $requestIndex }}

{{- range $messageIndex, $message := $webSocket.Messages }}
{{- $dropped := index $message.Metadata "dropped" }}

### {{ if $dropped }}~~Message {{ inc $messageIndex }}~~{{ else }}Message {{ inc $messageIndex }}{{ end }}

| Time | Direction | Opcode | Binary | Status |
|-|-|-:|:-:|-|
| {{ $message.CreatedAt.Local.Format "2006-01-02 15:04:05.000" }} | {{ $message.Direction }} | {{ $message.Opcode }} | {{ $message.IsBinary }} | {{ if $dropped }}dropped{{ if index $message.Metadata "injected" }}, {{ end }}{{ end }}{{ if index $message.Metadata "injected" }}injected{{ end }} |

```text
{{ truncate $message.Payload $.Metadata.TruncateLength | cleanPrint }}
```

{{- if $message.Metadata }}

#### Metadata

```json
{{ toJSON $message.Metadata }}
```
{{- end }}
{{- else }}

No WebSocket messages were captured.
{{- end }} {{/* test case websocket messages range */}}
{{- end }} {{/* test case websocket if */}}
{{- end }} {{/* test case requests range */}}
{{- end }} {{/* appendix test cases range */}}

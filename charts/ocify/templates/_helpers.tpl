{{/*
Expand the name of the chart.
*/}}
{{- define "ocify.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name (truncated to the 63-char DNS limit).
*/}}
{{- define "ocify.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart name and version as used by the chart label.
*/}}
{{- define "ocify.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "ocify.labels" -}}
helm.sh/chart: {{ include "ocify.chart" . }}
{{ include "ocify.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "ocify.selectorLabels" -}}
app.kubernetes.io/name: {{ include "ocify.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Service account name to use.
*/}}
{{- define "ocify.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "ocify.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Image reference: a digest pin wins, otherwise repository:tag, defaulting the tag
to the chart appVersion. The release pipeline pins the digest at publish time.
*/}}
{{- define "ocify.image" -}}
{{- $repo := .Values.image.repository -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" $repo .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" $repo (.Values.image.tag | default .Chart.AppVersion) -}}
{{- end -}}
{{- end }}

{{/*
Image for the `helm test` connection pod. tests.image.tag is pinned as
`tag@sha256:digest`, so this yields repository:tag@digest.
*/}}
{{- define "ocify.testImage" -}}
{{- $img := .Values.tests.image -}}
{{- printf "%s:%s" $img.repository $img.tag -}}
{{- end }}

{{/*
Whether the chart manages the auth Secret itself (auth.users set, no
existingSecret supplied).
*/}}
{{- define "ocify.managedAuthSecret" -}}
{{- if and .Values.auth.users (not .Values.auth.existingSecret) }}true{{- end }}
{{- end }}

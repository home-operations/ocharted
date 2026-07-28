{{- if include "ocharted.managedAuthSecret" . }}
apiVersion: v1
kind: Secret
metadata:
  name: {{ include "ocharted.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "ocharted.labels" . | nindent 4 }}
type: Opaque
stringData:
  auth: {{ .Values.auth.users | quote }}
{{- end }}

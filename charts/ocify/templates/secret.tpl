{{- if include "ocify.managedAuthSecret" . }}
apiVersion: v1
kind: Secret
metadata:
  name: {{ include "ocify.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "ocify.labels" . | nindent 4 }}
type: Opaque
stringData:
  auth: {{ .Values.auth.users | quote }}
{{- end }}

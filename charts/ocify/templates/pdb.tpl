{{- if .Values.podDisruptionBudget.enabled }}
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: {{ include "ocify.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "ocify.labels" . | nindent 4 }}
spec:
  {{- if .Values.podDisruptionBudget.maxUnavailable }}
  maxUnavailable: {{ .Values.podDisruptionBudget.maxUnavailable }}
  {{- else }}
  minAvailable: {{ .Values.podDisruptionBudget.minAvailable }}
  {{- end }}
  selector:
    matchLabels:
      {{- include "ocify.selectorLabels" . | nindent 6 }}
{{- end }}

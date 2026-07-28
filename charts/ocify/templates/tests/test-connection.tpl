apiVersion: v1
kind: Pod
metadata:
  name: {{ include "ocify.fullname" . }}-test-connection
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "ocify.labels" . | nindent 4 }}
  annotations:
    helm.sh/hook: test
    helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded
spec:
  restartPolicy: Never
  securityContext:
    {{- toYaml .Values.podSecurityContext | nindent 4 }}
  containers:
    - name: curl
      image: {{ include "ocify.testImage" . | quote }}
      imagePullPolicy: {{ .Values.tests.image.pullPolicy }}
      securityContext:
        {{- toYaml .Values.securityContext | nindent 8 }}
      command:
        - /bin/sh
        - -c
        - |
          set -eu
          base="http://{{ include "ocify.fullname" . }}:{{ .Values.service.port }}"
          echo "GET ${base}/readyz"
          body="$(curl -fsS "${base}/readyz")"
          echo "${body}"
          echo "${body}" | grep -q '"status":"ok"'
          {{- if and (not .Values.auth.users) (not .Values.auth.existingSecret) }}
          echo "GET ${base}/v2/"
          curl -fsS "${base}/v2/" | grep -q '{}'
          {{- else }}
          # Auth is enabled: the unauthenticated ping must be challenged.
          echo "GET ${base}/v2/ (expect 401)"
          code="$(curl -s -o /dev/null -w '%{http_code}' "${base}/v2/")"
          echo "HTTP ${code}"
          [ "${code}" = "401" ]
          {{- end }}

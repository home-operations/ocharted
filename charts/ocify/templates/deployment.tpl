apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "ocify.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "ocify.labels" . | nindent 4 }}
  {{- with .Values.deploymentAnnotations }}
  # Workload-level annotations — e.g. reloader.stakater.com/auto, which must sit
  # on the Deployment (not the pod) to roll it when a referenced object changes.
  annotations:
    {{- tpl (toYaml .) $ | nindent 4 }}
  {{- end }}
spec:
  replicas: {{ .Values.replicaCount }}
  {{- with .Values.strategy }}
  strategy:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  selector:
    matchLabels:
      {{- include "ocify.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "ocify.labels" . | nindent 8 }}
        {{- with .Values.podLabels }}
        {{- tpl (toYaml .) $ | nindent 8 }}
        {{- end }}
      {{- if or (include "ocify.managedAuthSecret" .) .Values.podAnnotations }}
      annotations:
        {{- if include "ocify.managedAuthSecret" . }}
        # Roll the pod when the chart-managed auth Secret changes (no-op for an
        # existingSecret — use Reloader via deploymentAnnotations there).
        checksum/secret: {{ include (print $.Template.BasePath "/secret.tpl") . | sha256sum }}
        {{- end }}
        {{- with .Values.podAnnotations }}
        {{- tpl (toYaml .) $ | nindent 8 }}
        {{- end }}
      {{- end }}
    spec:
      {{- with .Values.imagePullSecrets }}
      imagePullSecrets:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
      serviceAccountName: {{ include "ocify.serviceAccountName" . }}
      automountServiceAccountToken: {{ .Values.serviceAccount.automount }}
      {{- with .Values.priorityClassName }}
      priorityClassName: {{ tpl . $ | quote }}
      {{- end }}
      terminationGracePeriodSeconds: {{ .Values.terminationGracePeriodSeconds }}
      securityContext:
        {{- tpl (toYaml .Values.podSecurityContext) $ | nindent 8 }}
      {{- with .Values.initContainers }}
      initContainers:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
      containers:
        - name: ocify
          image: {{ include "ocify.image" . | quote }}
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          securityContext:
            {{- tpl (toYaml .Values.securityContext) $ | nindent 12 }}
          env:
            - name: OCIFY_PORT
              value: {{ .Values.config.port | quote }}
            - name: OCIFY_METRICS_ENABLED
              value: {{ .Values.config.metricsEnabled | quote }}
            - name: OCIFY_METRICS_PORT
              value: {{ .Values.config.metricsPort | quote }}
            - name: OCIFY_LOG_LEVEL
              value: {{ .Values.config.logLevel | quote }}
            - name: OCIFY_LOG_FORMAT
              value: {{ .Values.config.logFormat | quote }}
            - name: OCIFY_INDEX_TTL
              value: {{ .Values.config.indexTTL | quote }}
            - name: OCIFY_CACHE_MAX_BYTES
              value: {{ .Values.config.cacheMaxBytes | int64 | quote }}
            - name: OCIFY_MAX_INDEX_BYTES
              value: {{ .Values.config.maxIndexBytes | int64 | quote }}
            - name: OCIFY_MAX_CHART_BYTES
              value: {{ .Values.config.maxChartBytes | int64 | quote }}
            - name: OCIFY_UPSTREAM_TIMEOUT
              value: {{ .Values.config.upstreamTimeout | quote }}
            {{- with .Values.config.upstreamAllowlist }}
            - name: OCIFY_UPSTREAM_ALLOWLIST
              value: {{ join "," . | quote }}
            {{- end }}
            - name: OCIFY_ALLOW_PRIVATE_UPSTREAMS
              value: {{ .Values.config.allowPrivateUpstreams | quote }}
            - name: OCIFY_PROVENANCE_ENABLED
              value: {{ .Values.config.provenanceEnabled | quote }}
            - name: OCIFY_RESOLVE_SCAN_LIMIT
              value: {{ .Values.config.resolveScanLimit | quote }}
            - name: OCIFY_DISABLE_REQUEST_LOGS
              value: {{ .Values.config.disableRequestLogs | quote }}
            - name: OCIFY_SHUTDOWN_TIMEOUT
              value: {{ .Values.config.shutdownTimeout | quote }}
            {{- if or .Values.auth.users .Values.auth.existingSecret }}
            - name: OCIFY_AUTH
              valueFrom:
                secretKeyRef:
                  name: {{ .Values.auth.existingSecret | default (include "ocify.fullname" .) }}
                  key: {{ if .Values.auth.existingSecret }}{{ .Values.auth.existingSecretKey }}{{ else }}auth{{ end }}
            {{- end }}
            {{- if .Values.signing.existingSecret }}
            - name: OCIFY_SIGNING_KEY_PATH
              value: /etc/ocify/signing/{{ .Values.signing.existingSecretKey }}
            {{- end }}
            {{- if .Values.config.rewriteDependencies }}
            - name: OCIFY_REWRITE_DEPENDENCIES
              value: "true"
            - name: OCIFY_EXTERNAL_HOST
              value: {{ required "config.externalHost is required when config.rewriteDependencies is enabled" .Values.config.externalHost | quote }}
            {{- end }}
            {{- range $k, $v := .Values.env }}
            - name: {{ $k }}
              value: {{ tpl (toString $v) $ | quote }}
            {{- end }}
            {{- with .Values.extraEnv }}
            {{- tpl (toYaml .) $ | nindent 12 }}
            {{- end }}
          {{- with .Values.envFrom }}
          envFrom:
            {{- tpl (toYaml .) $ | nindent 12 }}
          {{- end }}
          ports:
            - name: http
              containerPort: {{ .Values.config.port }}
              protocol: TCP
            {{- if .Values.config.metricsEnabled }}
            - name: metrics
              containerPort: {{ .Values.config.metricsPort }}
              protocol: TCP
            {{- end }}
          {{- with .Values.startupProbe }}
          startupProbe:
            {{- tpl (toYaml .) $ | nindent 12 }}
          {{- end }}
          {{- with .Values.livenessProbe }}
          livenessProbe:
            {{- tpl (toYaml .) $ | nindent 12 }}
          {{- end }}
          {{- with .Values.readinessProbe }}
          readinessProbe:
            {{- tpl (toYaml .) $ | nindent 12 }}
          {{- end }}
          {{- with .Values.resources }}
          resources:
            {{- tpl (toYaml .) $ | nindent 12 }}
          {{- end }}
          {{- if or .Values.signing.existingSecret .Values.volumeMounts }}
          volumeMounts:
            {{- if .Values.signing.existingSecret }}
            - name: signing-key
              mountPath: /etc/ocify/signing
              readOnly: true
            {{- end }}
            {{- with .Values.volumeMounts }}
            {{- tpl (toYaml .) $ | nindent 12 }}
            {{- end }}
          {{- end }}
      {{- if or .Values.signing.existingSecret .Values.volumes }}
      volumes:
        {{- if .Values.signing.existingSecret }}
        - name: signing-key
          secret:
            secretName: {{ .Values.signing.existingSecret }}
        {{- end }}
        {{- with .Values.volumes }}
        {{- tpl (toYaml .) $ | nindent 8 }}
        {{- end }}
      {{- end }}
      {{- with .Values.nodeSelector }}
      nodeSelector:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
      {{- with .Values.affinity }}
      affinity:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
      {{- with .Values.tolerations }}
      tolerations:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}
      {{- with .Values.topologySpreadConstraints }}
      topologySpreadConstraints:
        {{- tpl (toYaml .) $ | nindent 8 }}
      {{- end }}

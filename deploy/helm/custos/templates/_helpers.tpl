{{- define "custos.labels" -}}
app.kubernetes.io/name: custos
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "custos.podSpec" -}}
securityContext:
  runAsNonRoot: true
  runAsUser: 65532
  runAsGroup: 65532
  seccompProfile:
    type: RuntimeDefault
containers:
  - name: custos
    image: "{{ .Values.image.repository }}:{{ .Values.image.tag }}"
    imagePullPolicy: {{ .Values.image.pullPolicy }}
    args: ["--config", "/etc/custos/config.yaml", {{ .component | quote }}]
    securityContext:
      allowPrivilegeEscalation: false
      readOnlyRootFilesystem: true
      capabilities:
        drop: ["ALL"]
    env:
      - name: CUSTOS_DATABASE__URL_FILE
        value: /etc/custos/secrets/database-url
    volumeMounts:
      - name: config
        mountPath: /etc/custos/config.yaml
        subPath: config.yaml
        readOnly: true
      - name: secrets
        mountPath: /etc/custos/secrets
        readOnly: true
      - name: tmp
        mountPath: /tmp
      {{- if .Values.tlsSecret.enabled }}
      - name: tls
        mountPath: /etc/custos/tls
        readOnly: true
      {{- end }}
    resources:
      {{- toYaml .Values.resources | nindent 6 }}
  {{- if .Values.validator.enabled }}
  - name: validator
    # Sidecar shares the pod network namespace: custos reaches it at
    # 127.0.0.1:8481 and it is never exposed (docs/script-validation.md).
    image: "{{ .Values.validator.image.repository }}:{{ .Values.validator.image.tag }}"
    imagePullPolicy: {{ .Values.validator.image.pullPolicy }}
    securityContext:
      allowPrivilegeEscalation: false
      readOnlyRootFilesystem: true
      capabilities:
        drop: ["ALL"]
    volumeMounts:
      - name: tmp
        mountPath: /tmp
    resources:
      {{- toYaml .Values.validator.resources | nindent 6 }}
    livenessProbe:
      httpGet:
        path: /healthz
        port: 8481
      periodSeconds: 15
  {{- end }}
    {{- if eq .component "serve" }}
    ports:
      - name: api
        containerPort: 8443
      - name: metrics
        containerPort: 9090
    livenessProbe:
      httpGet:
        path: /health/live
        port: api
        scheme: {{ ternary "HTTPS" "HTTP" (eq .Values.config.server.tls.mode "required") }}
      periodSeconds: 15
    readinessProbe:
      httpGet:
        path: /health/ready
        port: api
        scheme: {{ ternary "HTTPS" "HTTP" (eq .Values.config.server.tls.mode "required") }}
      periodSeconds: 10
    {{- end }}
volumes:
  - name: config
    configMap:
      name: {{ .Release.Name }}-custos-config
  - name: secrets
    secret:
      secretName: {{ .Values.databaseURLSecret.name }}
      items:
        - key: {{ .Values.databaseURLSecret.key }}
          path: database-url
  - name: tmp
    emptyDir: {}
  {{- if .Values.tlsSecret.enabled }}
  - name: tls
    secret:
      secretName: {{ .Values.tlsSecret.name }}
  {{- end }}
{{- end -}}

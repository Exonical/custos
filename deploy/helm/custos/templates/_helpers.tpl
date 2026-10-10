{{/*
Names
*/}}
{{- define "custos.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "custos.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "custos.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "custos.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "custos.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* Service names. The API service name is part of the API certificate SANs. */}}
{{- define "custos.apiService" -}}{{ include "custos.fullname" . }}-api{{- end -}}
{{- define "custos.workerMetricsService" -}}{{ include "custos.fullname" . }}-worker-metrics{{- end -}}
{{- define "custos.webService" -}}{{ include "custos.fullname" . }}-web{{- end -}}
{{- define "custos.apiHost" -}}{{ include "custos.apiService" . }}.{{ .Release.Namespace }}.svc{{- end -}}
{{- define "custos.apiURL" -}}
{{- if .Values.web.apiURL -}}{{ .Values.web.apiURL }}{{- else -}}https://{{ include "custos.apiHost" . }}:{{ .Values.api.service.port }}{{- end -}}
{{- end -}}

{{/*
Labels. component is one of serve, worker, web, migrate.
*/}}
{{- define "custos.labels" -}}
helm.sh/chart: {{ include "custos.chart" .root }}
{{ include "custos.selectorLabels" . }}
app.kubernetes.io/version: {{ .root.Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
app.kubernetes.io/part-of: custos
{{- end -}}

{{- define "custos.selectorLabels" -}}
app.kubernetes.io/name: {{ include "custos.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{/*
Image reference: digest wins over tag; tag defaults to the chart appVersion.
Usage: include "custos.image" (dict "image" .Values.image "root" $)
*/}}
{{- define "custos.image" -}}
{{- if .image.digest -}}
{{- printf "%s@%s" .image.repository .image.digest -}}
{{- else -}}
{{- printf "%s:%s" .image.repository (default .root.Chart.AppVersion .image.tag) -}}
{{- end -}}
{{- end -}}

{{- define "custos.imagePullSecrets" -}}
{{- with .Values.global.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{/*
Restricted Pod Security Standard.
*/}}
{{- define "custos.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: {{ .uid }}
runAsGroup: {{ .uid }}
fsGroup: {{ .uid }}
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "custos.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
runAsNonRoot: true
capabilities:
  drop:
    - ALL
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{/*
API TLS Secret name and the CA the web frontend uses to verify it.
*/}}
{{- define "custos.tlsSecretName" -}}
{{- default (printf "%s-api-tls" (include "custos.fullname" .)) .Values.api.tls.secretName -}}
{{- end -}}

{{- define "custos.apiCA" -}}
{{- if .Values.api.tls.caSecret.name -}}
{{- dict "name" .Values.api.tls.caSecret.name "key" (default "ca.crt" .Values.api.tls.caSecret.key) | toJson -}}
{{- else -}}
{{- dict "name" (include "custos.tlsSecretName" .) "key" "ca.crt" | toJson -}}
{{- end -}}
{{- end -}}

{{/*
Database wiring. With postgresql.enabled the CloudNativePG secrets are used:
<cluster>-app (key uri) and <cluster>-ca (key ca.crt).
*/}}
{{- define "custos.pgCluster" -}}
{{- default (printf "%s-postgresql" .Release.Name) .Values.postgresql.fullnameOverride -}}
{{- end -}}

{{- define "custos.db.migrateSecret" -}}
{{- if .Values.postgresql.enabled -}}
{{- dict "name" (printf "%s-app" (include "custos.pgCluster" .)) "key" "uri" | toJson -}}
{{- else -}}
{{- dict "name" .Values.database.migrateURLSecret.name "key" (default "url" .Values.database.migrateURLSecret.key) | toJson -}}
{{- end -}}
{{- end -}}

{{- define "custos.db.appSecret" -}}
{{- if .Values.postgresql.enabled -}}
{{- dict "name" (printf "%s-app" (include "custos.pgCluster" .)) "key" "uri" | toJson -}}
{{- else -}}
{{- dict "name" .Values.database.appURLSecret.name "key" (default "url" .Values.database.appURLSecret.key) | toJson -}}
{{- end -}}
{{- end -}}

{{- define "custos.db.caSecret" -}}
{{- if .Values.postgresql.enabled -}}
{{- dict "name" (printf "%s-ca" (include "custos.pgCluster" .)) "key" "ca.crt" | toJson -}}
{{- else if .Values.database.caSecret.name -}}
{{- dict "name" .Values.database.caSecret.name "key" (default "ca.crt" .Values.database.caSecret.key) | toJson -}}
{{- else -}}
{{- dict | toJson -}}
{{- end -}}
{{- end -}}

{{- define "custos.db.appRole" -}}
{{- if .Values.postgresql.enabled -}}{{- else -}}{{ .Values.database.appRole }}{{- end -}}
{{- end -}}

{{/*
Fail early with actionable messages.
*/}}
{{- define "custos.validate" -}}
{{- if not (or .Values.api.tls.secretName .Values.api.tls.certManager.enabled) -}}
{{- fail "API TLS is required: set api.tls.secretName (kubernetes.io/tls Secret) or api.tls.certManager.enabled=true with api.tls.certManager.issuerRef" -}}
{{- end -}}
{{- if and .Values.api.tls.certManager.enabled (not .Values.api.tls.certManager.issuerRef.name) -}}
{{- fail "api.tls.certManager.issuerRef.name is required when api.tls.certManager.enabled=true" -}}
{{- end -}}
{{- $oidc := default (dict) (get (default (dict) .Values.config.auth) "oidc") -}}
{{- if and (not .Values.config.dev_mode) (not (get $oidc "issuer")) -}}
{{- fail "config.auth.oidc.issuer is required unless config.dev_mode is true" -}}
{{- end -}}
{{- if not .Values.postgresql.enabled -}}
{{- if or (not .Values.database.migrateURLSecret.name) (not .Values.database.appURLSecret.name) -}}
{{- fail "database.migrateURLSecret.name and database.appURLSecret.name are required (or set postgresql.enabled=true to use the bundled CloudNativePG cluster)" -}}
{{- end -}}
{{- end -}}
{{- if .Values.web.enabled -}}
{{- if not .Values.web.publicOrigin -}}
{{- fail "web.publicOrigin is required when web.enabled=true (the browser-facing origin, for example https://custos.example.org)" -}}
{{- end -}}
{{- if or (not .Values.web.client.secret.name) (not .Values.web.authSecrets.name) -}}
{{- fail "web.client.secret.name and web.authSecrets.name are required when web.enabled=true" -}}
{{- end -}}
{{- if not (or .Values.web.issuer (get $oidc "issuer")) -}}
{{- fail "web.issuer or config.auth.oidc.issuer is required when web.enabled=true" -}}
{{- end -}}
{{- end -}}
{{- if .Values.openbaoClient.enabled -}}
{{- $wi := .Values.openbaoClient.auth.workloadIdentity.enabled -}}
{{- $cc := .Values.openbaoClient.auth.oidcClientCredentials.enabled -}}
{{- if not .Values.openbaoClient.address -}}
{{- fail "openbaoClient.address is required when openbaoClient.enabled=true" -}}
{{- end -}}
{{- if eq (toString $wi) (toString $cc) -}}
{{- fail "enable exactly one of openbaoClient.auth.workloadIdentity.enabled and openbaoClient.auth.oidcClientCredentials.enabled" -}}
{{- end -}}
{{- if and $cc (or (not .Values.openbaoClient.auth.oidcClientCredentials.tokenURL) (not .Values.openbaoClient.auth.oidcClientCredentials.clientID) (not .Values.openbaoClient.auth.oidcClientCredentials.clientSecret.name)) -}}
{{- fail "openbaoClient.auth.oidcClientCredentials needs tokenURL, clientID and clientSecret.name" -}}
{{- end -}}
{{- end -}}
{{- if and .Values.gateway.enabled (not .Values.gateway.parentRefs) -}}
{{- fail "gateway.parentRefs is required when gateway.enabled=true" -}}
{{- end -}}
{{- if and .Values.gateway.enabled .Values.gateway.api.backendTLS.enabled (not .Values.gateway.api.backendTLS.caConfigMap) -}}
{{- fail "gateway.api.backendTLS.caConfigMap is required when backendTLS is enabled" -}}
{{- end -}}
{{- end -}}

{{/*
The rendered config.yaml. User config (passthrough) is deep-merged under the
chart-managed keys, so the chart wins. Values that can legitimately be false
are set with `set` because mergeOverwrite ignores zero values in the source.
*/}}
{{- define "custos.config" -}}
{{- $cfg := deepCopy (default (dict) .Values.config) -}}
{{- $server := default (dict) (get $cfg "server") -}}
{{- $_ := set $server "listen" ":8443" -}}
{{- $tls := default (dict) (get $server "tls") -}}
{{- $_ = set $tls "mode" "required" -}}
{{- $_ = set $tls "cert_file" "/etc/custos/tls/tls.crt" -}}
{{- $_ = set $tls "key_file" "/etc/custos/tls/tls.key" -}}
{{- $_ = set $server "tls" $tls -}}
{{- $_ = set $cfg "server" $server -}}
{{- $metrics := default (dict) (get $cfg "metrics") -}}
{{- $_ = set $metrics "enabled" true -}}
{{- $_ = set $metrics "listen" ":9090" -}}
{{- $_ = set $metrics "tls" (dict "mode" "upstream") -}}
{{- $_ = set $cfg "metrics" $metrics -}}
{{- $database := default (dict) (get $cfg "database") -}}
{{- $_ = set $database "ssl_mode" .Values.database.sslMode -}}
{{- $dbCA := include "custos.db.caSecret" . | fromJson -}}
{{- if $dbCA.name -}}
{{- $dbTLS := default (dict) (get $database "tls") -}}
{{- $_ = set $dbTLS "root_ca_file" "/etc/custos/ca/database-ca.crt" -}}
{{- $_ = set $database "tls" $dbTLS -}}
{{- end -}}
{{- $_ = set $cfg "database" $database -}}
{{- $auth := default (dict) (get $cfg "auth") -}}
{{- $oidc := default (dict) (get $auth "oidc") -}}
{{- if .Values.auth.oidc.caSecret.name -}}
{{- $_ = set $oidc "ca_file" "/etc/custos/ca/oidc-ca.crt" -}}
{{- end -}}
{{- $_ = set $auth "oidc" $oidc -}}
{{- $_ = set $cfg "auth" $auth -}}
{{- $validation := default (dict) (get $cfg "validation") -}}
{{- $shellcheck := default (dict) (get $validation "shellcheck") -}}
{{- $_ = set $shellcheck "enabled" .Values.validator.enabled -}}
{{- $_ = set $shellcheck "endpoint" "http://127.0.0.1:8481" -}}
{{- $_ = set $validation "shellcheck" $shellcheck -}}
{{- $_ = set $cfg "validation" $validation -}}
{{- if .Values.openbaoClient.enabled -}}
{{- $secrets := default (dict) (get $cfg "secrets") -}}
{{- $bao := default (dict) (get $secrets "openbao") -}}
{{- $_ = set $bao "address" .Values.openbaoClient.address -}}
{{- $_ = set $bao "timeout" .Values.openbaoClient.timeout -}}
{{- if .Values.openbaoClient.namespace -}}
{{- $_ = set $bao "namespace" .Values.openbaoClient.namespace -}}
{{- end -}}
{{- if .Values.openbaoClient.caSecret.name -}}
{{- $_ = set $bao "ca_file" "/etc/custos/ca/openbao-ca.crt" -}}
{{- end -}}
{{- $jwt := dict "role" .Values.openbaoClient.auth.role -}}
{{- if .Values.openbaoClient.auth.workloadIdentity.enabled -}}
{{- $_ = set $jwt "token_file" "/var/run/secrets/custos/openbao/token" -}}
{{- else -}}
{{- $_ = set $jwt "oidc_client_credentials" (dict "token_url" .Values.openbaoClient.auth.oidcClientCredentials.tokenURL "client_id" .Values.openbaoClient.auth.oidcClientCredentials.clientID) -}}
{{- end -}}
{{- $_ = set $bao "auth" (dict "method" "jwt" "jwt" $jwt) -}}
{{- $_ = set $secrets "openbao" $bao -}}
{{- $_ = set $cfg "secrets" $secrets -}}
{{- end -}}
{{- toYaml $cfg -}}
{{- end -}}

{{/*
Projected secrets volume for the Custos container: database URL (app or
migrate), optional OIDC client secret and OpenBao client secret.
Usage: include "custos.secretsVolume" (dict "root" $ "db" "app")
*/}}
{{- define "custos.secretsVolume" -}}
{{- $root := .root -}}
{{- $db := ternary (include "custos.db.migrateSecret" $root | fromJson) (include "custos.db.appSecret" $root | fromJson) (eq .db "migrate") -}}
- name: secrets
  projected:
    defaultMode: 288
    sources:
      - secret:
          name: {{ $db.name }}
          items:
            - key: {{ $db.key }}
              path: database-url
      {{- if $root.Values.auth.oidc.clientSecret.name }}
      - secret:
          name: {{ $root.Values.auth.oidc.clientSecret.name }}
          items:
            - key: {{ $root.Values.auth.oidc.clientSecret.key }}
              path: oidc-client-secret
      {{- end }}
      {{- if and $root.Values.openbaoClient.enabled $root.Values.openbaoClient.auth.oidcClientCredentials.enabled }}
      - secret:
          name: {{ $root.Values.openbaoClient.auth.oidcClientCredentials.clientSecret.name }}
          items:
            - key: {{ $root.Values.openbaoClient.auth.oidcClientCredentials.clientSecret.key }}
              path: openbao-client-secret
      {{- end }}
{{- end -}}

{{/* Projected CA bundle volume (database, OIDC issuer, OpenBao). */}}
{{- define "custos.caVolume" -}}
{{- $db := include "custos.db.caSecret" . | fromJson -}}
{{- if or $db.name .Values.auth.oidc.caSecret.name (and .Values.openbaoClient.enabled .Values.openbaoClient.caSecret.name) }}
- name: ca
  projected:
    defaultMode: 292
    sources:
      {{- if $db.name }}
      - secret:
          name: {{ $db.name }}
          items:
            - key: {{ $db.key }}
              path: database-ca.crt
      {{- end }}
      {{- if .Values.auth.oidc.caSecret.name }}
      - secret:
          name: {{ .Values.auth.oidc.caSecret.name }}
          items:
            - key: {{ default "ca.crt" .Values.auth.oidc.caSecret.key }}
              path: oidc-ca.crt
      {{- end }}
      {{- if and .Values.openbaoClient.enabled .Values.openbaoClient.caSecret.name }}
      - secret:
          name: {{ .Values.openbaoClient.caSecret.name }}
          items:
            - key: {{ default "ca.crt" .Values.openbaoClient.caSecret.key }}
              path: openbao-ca.crt
      {{- end }}
{{- end }}
{{- end -}}

{{/* Whether the ca volume exists (same condition as custos.caVolume). */}}
{{- define "custos.hasCAVolume" -}}
{{- $db := include "custos.db.caSecret" . | fromJson -}}
{{- if or $db.name .Values.auth.oidc.caSecret.name (and .Values.openbaoClient.enabled .Values.openbaoClient.caSecret.name) -}}true{{- end -}}
{{- end -}}

{{/*
Volumes shared by every Custos container (serve, worker, migrate).
Usage: include "custos.volumes" (dict "root" $ "db" "app" "tls" true)
*/}}
{{- define "custos.volumes" -}}
- name: config
  configMap:
    name: {{ include "custos.fullname" .root }}-config
{{ include "custos.secretsVolume" (dict "root" .root "db" .db) }}
{{- include "custos.caVolume" .root }}
- name: tmp
  emptyDir:
    sizeLimit: 64Mi
{{- if .tls }}
- name: tls
  secret:
    secretName: {{ include "custos.tlsSecretName" .root }}
    defaultMode: 288
{{- end }}
{{- if and .root.Values.openbaoClient.enabled .root.Values.openbaoClient.auth.workloadIdentity.enabled }}
- name: openbao-token
  projected:
    defaultMode: 288
    sources:
      - serviceAccountToken:
          audience: {{ .root.Values.openbaoClient.auth.workloadIdentity.audience }}
          expirationSeconds: {{ .root.Values.openbaoClient.auth.workloadIdentity.expirationSeconds }}
          path: token
{{- end }}
{{- end -}}

{{- define "custos.volumeMounts" -}}
- name: config
  mountPath: /etc/custos/config.yaml
  subPath: config.yaml
  readOnly: true
- name: secrets
  mountPath: /etc/custos/secrets
  readOnly: true
{{- if include "custos.hasCAVolume" .root }}
- name: ca
  mountPath: /etc/custos/ca
  readOnly: true
{{- end }}
- name: tmp
  mountPath: /tmp
{{- if .tls }}
- name: tls
  mountPath: /etc/custos/tls
  readOnly: true
{{- end }}
{{- if and .root.Values.openbaoClient.enabled .root.Values.openbaoClient.auth.workloadIdentity.enabled }}
- name: openbao-token
  mountPath: /var/run/secrets/custos/openbao
  readOnly: true
{{- end }}
{{- end -}}

{{/* Environment shared by every Custos container. */}}
{{- define "custos.env" -}}
- name: CUSTOS_DATABASE__URL_FILE
  value: /etc/custos/secrets/database-url
{{- if .root.Values.auth.oidc.clientSecret.name }}
- name: CUSTOS_AUTH__OIDC__CLIENT_SECRET_FILE
  value: /etc/custos/secrets/oidc-client-secret
{{- end }}
{{- if and .root.Values.openbaoClient.enabled .root.Values.openbaoClient.auth.oidcClientCredentials.enabled }}
- name: CUSTOS_SECRETS__OPENBAO__AUTH__JWT__OIDC_CLIENT_CREDENTIALS__CLIENT_SECRET_FILE
  value: /etc/custos/secrets/openbao-client-secret
{{- end }}
{{- end -}}

{{/*
`custos migrate wait` init container (regular init container, runs after the
validator native sidecar has started).
*/}}
{{- define "custos.migrateWait" -}}
- name: migrate-wait
  image: {{ include "custos.image" (dict "image" .Values.image "root" .) | quote }}
  imagePullPolicy: {{ .Values.image.pullPolicy }}
  args:
    - --config
    - /etc/custos/config.yaml
    - migrate
    - wait
    - --timeout
    - {{ .Values.migrations.waitTimeout | quote }}
  env:
    {{- include "custos.env" (dict "root" .) | nindent 4 }}
  securityContext:
    {{- include "custos.containerSecurityContext" . | nindent 4 }}
  resources:
    requests:
      cpu: 10m
      memory: 32Mi
    limits:
      memory: 128Mi
  volumeMounts:
    {{- include "custos.volumeMounts" (dict "root" . "tls" false) | nindent 4 }}
{{- end -}}

{{/*
ShellCheck validator as a native sidecar: an init container with
restartPolicy Always (Kubernetes >= 1.29). Loopback only; never exposed.
*/}}
{{- define "custos.validatorSidecar" -}}
- name: validator
  image: {{ include "custos.image" (dict "image" .Values.validator.image "root" .) | quote }}
  imagePullPolicy: {{ .Values.validator.image.pullPolicy }}
  restartPolicy: Always
  securityContext:
    {{- include "custos.containerSecurityContext" . | nindent 4 }}
  resources:
    {{- toYaml .Values.validator.resources | nindent 4 }}
  # The validator listens on loopback only, which the kubelet cannot reach with
  # httpGet/tcpSocket probes, so liveness execs the binary's healthcheck.
  livenessProbe:
    exec:
      command: [/custos-validator, healthcheck]
    periodSeconds: 15
    timeoutSeconds: 5
  volumeMounts:
    - name: tmp
      mountPath: /tmp
{{- end -}}

{{/*
Topology spread: the component's own constraints, else a soft per-host spread.
Usage: include "custos.topologySpread" (dict "root" $ "component" "serve" "spec" .Values.serve)
*/}}
{{- define "custos.topologySpread" -}}
{{- if .spec.topologySpreadConstraints }}
topologySpreadConstraints:
  {{- toYaml .spec.topologySpreadConstraints | nindent 2 }}
{{- else if .root.Values.topologySpread.enabled }}
topologySpreadConstraints:
  - maxSkew: 1
    topologyKey: kubernetes.io/hostname
    whenUnsatisfiable: ScheduleAnyway
    labelSelector:
      matchLabels:
        {{- include "custos.selectorLabels" (dict "root" .root "component" .component) | nindent 8 }}
{{- end }}
{{- end -}}

{{/* nodeSelector / tolerations / affinity from a component spec. */}}
{{- define "custos.scheduling" -}}
{{- with .nodeSelector }}
nodeSelector:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .tolerations }}
tolerations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .affinity }}
affinity:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{/*
Restricted egress (networkPolicy.egress.restrict=true): DNS, pods of this
release (web -> api), the bundled postgres/openbao pods and the raw extras.
*/}}
{{- define "custos.egressRules" -}}
- to:
    - namespaceSelector:
        {{- toYaml .Values.networkPolicy.egress.dnsNamespaceSelector | nindent 8 }}
  ports:
    - port: 53
      protocol: UDP
    - port: 53
      protocol: TCP
- to:
    - podSelector:
        matchLabels:
          app.kubernetes.io/instance: {{ .Release.Name }}
          app.kubernetes.io/part-of: custos
  ports:
    - port: 8443
      protocol: TCP
{{- if .Values.postgresql.enabled }}
- to:
    - podSelector:
        matchLabels:
          cnpg.io/cluster: {{ include "custos.pgCluster" . }}
  ports:
    - port: 5432
      protocol: TCP
{{- end }}
{{- if .Values.openbao.enabled }}
- to:
    - podSelector:
        matchLabels:
          app.kubernetes.io/name: openbao
          app.kubernetes.io/instance: {{ .Release.Name }}
  ports:
    - port: 8200
      protocol: TCP
{{- end }}
{{- with .Values.networkPolicy.egress.extra }}
{{ toYaml . }}
{{- end }}
{{- end -}}

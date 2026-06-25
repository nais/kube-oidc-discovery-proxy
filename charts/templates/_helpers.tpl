{{/*
Expand the name of the chart.
*/}}
{{- define "kube-oidc-discovery-proxy.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "kube-oidc-discovery-proxy.fullname" -}}
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
Create chart name and version as used by the chart label.
*/}}
{{- define "kube-oidc-discovery-proxy.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "kube-oidc-discovery-proxy.labels" -}}
helm.sh/chart: {{ include "kube-oidc-discovery-proxy.chart" . }}
{{ include "kube-oidc-discovery-proxy.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "kube-oidc-discovery-proxy.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kube-oidc-discovery-proxy.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "kube-oidc-discovery-proxy.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "kube-oidc-discovery-proxy.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Short, predictable name for an upstream apiserver host. Uses the second DNS
label, e.g. apiserver.dev-fss.nais.io -> dev-fss.
*/}}
{{- define "kube-oidc-discovery-proxy.upstreamName" -}}
{{- $labels := splitList "." . -}}
{{- if gt (len $labels) 1 -}}
{{- index $labels 1 -}}
{{- else -}}
{{- index $labels 0 -}}
{{- end -}}
{{- end }}

{{/*
Predictable ingress host for an upstream apiserver host, e.g.
apiserver.dev-fss.nais.io -> apiserver-oidc-dev-fss.external.nav.cloud.nais.io
*/}}
{{- define "kube-oidc-discovery-proxy.ingressHost" -}}
{{- printf "apiserver-oidc-%s.%s" (include "kube-oidc-discovery-proxy.upstreamName" .upstream) .domain -}}
{{- end }}

{{/* skeleton.fullname is the name of the objects of the release. */}}
{{- define "skeleton.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
skeleton.clusterName is the name of the cluster-scoped objects of the
release. Two releases of one name in two namespaces must not share them.
*/}}
{{- define "skeleton.clusterName" -}}
{{- printf "%s-%s" .Release.Namespace .Release.Name | trunc 57 | trimSuffix "-" -}}
{{- end -}}

{{- define "skeleton.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "skeleton.labels" -}}
{{ include "skeleton.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

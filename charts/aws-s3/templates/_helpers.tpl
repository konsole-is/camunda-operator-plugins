{{/* aws-s3.fullname is the name of the objects of the release. */}}
{{- define "aws-s3.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
aws-s3.clusterName is the name of the cluster-scoped objects of the
release, at most 57 characters. Two releases in two namespaces must not share
them, so the name ends with a hash of namespace/release: the readable part can
be the same for two releases, and it is cut at 48 characters.
*/}}
{{- define "aws-s3.clusterName" -}}
{{- $id := printf "%s/%s" .Release.Namespace .Release.Name -}}
{{- printf "%s-%s" .Release.Namespace .Release.Name | trunc 48 | trimSuffix "-" -}}
-{{- $id | sha256sum | trunc 8 -}}
{{- end -}}

{{- define "aws-s3.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "aws-s3.labels" -}}
{{ include "aws-s3.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{/* aws-s3.extraTags is the extraTags of the values as key=value items, sorted by key. */}}
{{- define "aws-s3.extraTags" -}}
{{- $items := list }}
{{- range $key, $value := .Values.extraTags }}
{{- if contains "=" $key }}
{{- fail (printf "extraTags: the key %q has a \"=\", which the flag -extra-tags cannot carry" $key) }}
{{- end }}
{{- $items = append $items (printf "%s=%v" $key $value) }}
{{- end }}
{{- join "," $items }}
{{- end -}}

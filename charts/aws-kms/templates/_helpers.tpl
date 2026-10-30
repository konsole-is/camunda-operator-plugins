{{/* aws-kms.fullname is the name of the objects of the release. */}}
{{- define "aws-kms.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
aws-kms.clusterName is the name of the cluster-scoped objects of the
release, at most 57 characters. Two releases in two namespaces must not share
them, so the name ends with a hash of namespace/release: the readable part can
be the same for two releases, and it is cut at 48 characters.
*/}}
{{- define "aws-kms.clusterName" -}}
{{- $id := printf "%s/%s" .Release.Namespace .Release.Name -}}
{{- printf "%s-%s" .Release.Namespace .Release.Name | trunc 48 | trimSuffix "-" -}}
-{{- $id | sha256sum | trunc 8 -}}
{{- end -}}

{{- define "aws-kms.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "aws-kms.labels" -}}
{{ include "aws-kms.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{/*
aws-kms.registrations is the list of the registrations of the values, each
with the limit Alias prefix added to its limits.
*/}}
{{- define "aws-kms.registrations" -}}
{{- $prefix := dict "label" "Alias prefix" "value" .Values.aliasPrefix "description" "The prefix of the alias of each key, after alias/." }}
{{- $out := list }}
{{- range .Values.registrations }}
{{- $registration := deepCopy . }}
{{- $_ := set $registration "limits" (append (default (list) $registration.limits) $prefix) }}
{{- $out = append $out $registration }}
{{- end }}
{{- toYaml $out }}
{{- end -}}

{{/* aws-kms.extraTags is the extraTags of the values as key=value items, sorted by key. */}}
{{- define "aws-kms.extraTags" -}}
{{- $items := list }}
{{- range $key, $value := .Values.extraTags }}
{{- $items = append $items (printf "%s=%v" $key $value) }}
{{- end }}
{{- join "," $items }}
{{- end -}}

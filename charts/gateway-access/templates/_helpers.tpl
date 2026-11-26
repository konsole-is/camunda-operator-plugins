{{/* gateway-access.fullname is the name of the objects of the release. */}}
{{- define "gateway-access.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
gateway-access.clusterName is the name of the cluster-scoped objects of the
release, at most 57 characters. Two releases in two namespaces must not share
them, so the name ends with a hash of namespace/release: the readable part can
be the same for two releases, and it is cut at 48 characters.
*/}}
{{- define "gateway-access.clusterName" -}}
{{- $id := printf "%s/%s" .Release.Namespace .Release.Name -}}
{{- printf "%s-%s" .Release.Namespace .Release.Name | trunc 48 | trimSuffix "-" -}}
-{{- $id | sha256sum | trunc 8 -}}
{{- end -}}

{{- define "gateway-access.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "gateway-access.labels" -}}
{{ include "gateway-access.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{/*
gateway-access.policyKinds is the value of the flag -policy-kinds: each item of
policyKinds as <apiVersion>/<kind>, separated by commas. It fails on an item
that the controller or the policy role cannot use.
*/}}
{{- define "gateway-access.policyKinds" -}}
{{- $kinds := list -}}
{{- range .Values.policyKinds -}}
{{- $apiVersion := required "each policy kind needs an apiVersion" .apiVersion -}}
{{- $kind := required "each policy kind needs a kind" .kind -}}
{{- $_ := required "each policy kind needs a resource" .resource -}}
{{- $parts := splitList "/" $apiVersion -}}
{{- if or (ne (len $parts) 2) (not (first $parts)) (not (last $parts)) -}}
{{- fail (printf "the policy kind %s needs an apiVersion <group>/<version>" $kind) -}}
{{- end -}}
{{- $kinds = append $kinds (printf "%s/%s" $apiVersion $kind) -}}
{{- end -}}
{{- join "," $kinds -}}
{{- end -}}

{{/* aws-connectors-role.fullname is the name of the objects of the release. */}}
{{- define "aws-connectors-role.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
aws-connectors-role.clusterName is the name of the cluster-scoped objects of the
release, at most 57 characters. Two releases in two namespaces must not share
them, so the name ends with a hash of namespace/release: the readable part can
be the same for two releases, and it is cut at 48 characters.
*/}}
{{- define "aws-connectors-role.clusterName" -}}
{{- $id := printf "%s/%s" .Release.Namespace .Release.Name -}}
{{- printf "%s-%s" .Release.Namespace .Release.Name | trunc 48 | trimSuffix "-" -}}
-{{- $id | sha256sum | trunc 8 -}}
{{- end -}}

{{- define "aws-connectors-role.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "aws-connectors-role.labels" -}}
{{ include "aws-connectors-role.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{/*
aws-connectors-role.extraTags is the value of the flag -extra-tags: the
extraTags of the values as key=value items, sorted by key, joined by ",".
*/}}
{{- define "aws-connectors-role.extraTags" -}}
{{- $items := list }}
{{- range $key, $value := .Values.extraTags }}
{{- $items = append $items (printf "%s=%s" $key $value) }}
{{- end }}
{{- join "," $items -}}
{{- end -}}

{{/*
aws-connectors-role.registrations is the list of the registrations of the
values, each merged over the defaults of the chart. The registration
aws-connectors-role also gets the permissions boundary as a limit. It fails
on an unknown mode.
*/}}
{{- define "aws-connectors-role.registrations" -}}
{{- if not (has .Values.mode (list "irsa" "podIdentity")) }}
{{- fail (printf "mode must be irsa or podIdentity, not %q" .Values.mode) }}
{{- end }}
{{- $defaults := dict
  "sections" (list "connectors-permissions")
  "docsUrl" "https://github.com/konsole-is/camunda-operator-plugins/blob/main/docs/plugins/aws-connectors-role.md"
  "mode" "Optional"
  "deletionPolicies" (list "Delete" "Orphan")
  "outputs" (list
    (dict "key" "principal" "format" "aws-iam-role-arn" "description" "The ARN of the IAM role of the Connectors.")
    (dict "key" "serviceAccountAnnotations" "description" "The annotations, as a JSON object, that bind the Connectors ServiceAccount to the role.")) }}
{{- $out := list }}
{{- range .Values.registrations }}
{{- $registration := deepCopy $defaults }}
{{- if and (eq .name "aws-connectors-role") $.Values.permissionsBoundaryArn }}
{{- $_ := set $registration "limits" (list (dict "label" "Permissions boundary" "value" $.Values.permissionsBoundaryArn "description" "The managed policy that bounds the role of the Connectors of each cluster.")) }}
{{- end }}
{{- $out = append $out (mergeOverwrite $registration .) }}
{{- end }}
{{- toYaml $out }}
{{- end -}}

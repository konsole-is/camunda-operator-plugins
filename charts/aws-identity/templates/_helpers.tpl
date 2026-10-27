{{/* aws-identity.fullname is the name of the objects of the release. */}}
{{- define "aws-identity.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
aws-identity.clusterName is the name of the cluster-scoped objects of the
release, at most 57 characters. Two releases in two namespaces must not share
them, so the name ends with a hash of namespace/release: the readable part can
be the same for two releases, and it is cut at 48 characters.
*/}}
{{- define "aws-identity.clusterName" -}}
{{- $id := printf "%s/%s" .Release.Namespace .Release.Name -}}
{{- printf "%s-%s" .Release.Namespace .Release.Name | trunc 48 | trimSuffix "-" -}}
-{{- $id | sha256sum | trunc 8 -}}
{{- end -}}

{{- define "aws-identity.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "aws-identity.labels" -}}
{{ include "aws-identity.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{/*
aws-identity.registrations is the list of the registrations of the values,
each merged over the defaults of the mode. It fails on an unknown mode, and
on a registration with the default name of the other mode.
*/}}
{{- define "aws-identity.registrations" -}}
{{- if not (has .Values.mode (list "irsa" "podIdentity")) }}
{{- fail (printf "mode must be irsa or podIdentity, not %q" .Values.mode) }}
{{- end }}
{{- $label := "AWS IAM role (IRSA)" }}
{{- $description := "Gives the cluster one AWS IAM role that its pods assume through IAM roles for service accounts." }}
{{- if eq .Values.mode "podIdentity" }}
{{- $label = "AWS IAM role (EKS Pod Identity)" }}
{{- $description = "Gives the cluster one AWS IAM role that its pods assume through EKS Pod Identity." }}
{{- end }}
{{- $limits := list }}
{{- with .Values.permissionsBoundaryArn }}
{{- $limits = append $limits (dict "label" "Permissions boundary" "value" . "description" "The managed policy that bounds every role.") }}
{{- end }}
{{- with .Values.allowedActions }}
{{- $limits = append $limits (dict "label" "Allowed actions" "value" (join ", " .) "description" "The IAM actions that a grant of another plugin can put on a role.") }}
{{- end }}
{{- $defaults := dict
  "sections" (list "cloud-identity")
  "label" $label
  "description" $description
  "docsUrl" "https://github.com/konsole-is/camunda-operator-plugins/blob/main/docs/plugins/aws-identity.md"
  "mode" "Optional"
  "deletionPolicies" (list "Delete" "Orphan")
  "outputs" (list
    (dict "key" "principal" "format" "aws-iam-role-arn" "description" "The ARN of the IAM role of the cluster.")
    (dict "key" "serviceAccountAnnotations" "description" "The annotations, as a JSON object, that bind a ServiceAccount to the role."))
  "limits" $limits }}
{{- $out := list }}
{{- range .Values.registrations }}
{{- if and (eq .name "aws-pod-identity") (eq $.Values.mode "irsa") }}
{{- fail "the registration aws-pod-identity needs mode podIdentity" }}
{{- end }}
{{- if and (eq .name "aws-irsa") (eq $.Values.mode "podIdentity") }}
{{- fail "the registration aws-irsa needs mode irsa" }}
{{- end }}
{{- $out = append $out (mergeOverwrite (deepCopy $defaults) .) }}
{{- end }}
{{- toYaml $out }}
{{- end -}}

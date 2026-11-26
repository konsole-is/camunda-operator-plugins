{{/* aws.fullname is the name of the objects of the release. */}}
{{- define "aws.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
aws.clusterName is the name of the cluster-scoped objects of the release, at
most 57 characters. Two releases in two namespaces must not share them, so
the name ends with a hash of namespace/release: the readable part can be the
same for two releases, and it is cut at 48 characters.
*/}}
{{- define "aws.clusterName" -}}
{{- $id := printf "%s/%s" .Release.Namespace .Release.Name -}}
{{- printf "%s-%s" .Release.Namespace .Release.Name | trunc 48 | trimSuffix "-" -}}
-{{- $id | sha256sum | trunc 8 -}}
{{- end -}}

{{- define "aws.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "aws.labels" -}}
{{ include "aws.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{/*
aws.blocks is a JSON list of the controller blocks that are on, in the order
identity, connectorsRole, kms, ebs, s3. Each item holds key, the key of the
block in the values, and name, the key in kebab case, which starts the flags
and the object names of the controller.
*/}}
{{- define "aws.blocks" -}}
{{- $out := list }}
{{- range list (list "identity" "identity") (list "connectorsRole" "connectors-role") (list "kms" "kms") (list "ebs" "ebs") (list "s3" "s3") }}
{{- $values := index $.Values (index . 0) | default dict }}
{{- if $values.enabled }}
{{- $out = append $out (dict "key" (index . 0) "name" (index . 1)) }}
{{- end }}
{{- end }}
{{- toJson $out }}
{{- end -}}

{{/*
aws.check fails the render without a controller that is on, for a block that
is on without a registration, and for a registration name in two blocks that
are on. A serve rule without resourceNames would grant serve on every
registration, and two controllers would serve the requests of one.
*/}}
{{- define "aws.check" -}}
{{- $blocks := include "aws.blocks" . | fromJsonArray }}
{{- if not $blocks }}
{{- fail "turn on at least one controller: identity.enabled, connectorsRole.enabled, kms.enabled, ebs.enabled, or s3.enabled" }}
{{- end }}
{{- $owners := dict }}
{{- range $block := $blocks }}
{{- $values := index $.Values $block.key }}
{{- if not $values.registrations }}
{{- fail (printf "%s.registrations must name at least one registration" $block.key) }}
{{- end }}
{{- range $values.registrations }}
{{- $name := required "each registration needs a name" .name }}
{{- with index $owners $name }}
{{- fail (printf "the registration %q is in both %s and %s" $name . $block.key) }}
{{- end }}
{{- $_ := set $owners $name $block.key }}
{{- end }}
{{- end }}
{{- end -}}

{{/*
aws.extraTags is the value of the flag -extra-tags: the extraTags of the
values as key=value items, sorted by key, joined by ",".
*/}}
{{- define "aws.extraTags" -}}
{{- $items := list }}
{{- range $key, $value := .Values.extraTags }}
{{- if contains "=" $key }}
{{- fail (printf "extraTags: the key %q has a \"=\", which the flag -extra-tags cannot carry" $key) }}
{{- end }}
{{- $items = append $items (printf "%s=%v" $key $value) }}
{{- end }}
{{- join "," $items -}}
{{- end -}}

{{/*
aws.registrationsData is the data of the registrations ConfigMap: one file
<name>.yaml for each block that is on, with the registrations of the block.
*/}}
{{- define "aws.registrationsData" -}}
{{- range include "aws.blocks" . | fromJsonArray }}
{{ .name }}.yaml: |
  {{- include (printf "aws.%sRegistrations" .key) $ | nindent 2 }}
{{- end }}
{{- end -}}

{{/*
aws.identityRegistrations is the list of the registrations of the identity
block, each merged over the defaults of the mode. It fails on an unknown
mode, and on a registration with the default name of the other mode.
*/}}
{{- define "aws.identityRegistrations" -}}
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
{{- with .Values.identity.permissionsBoundaryArn }}
{{- $limits = append $limits (dict "label" "Permissions boundary" "value" . "description" "The managed policy that bounds every role.") }}
{{- end }}
{{- with .Values.identity.allowedActions }}
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
{{- range .Values.identity.registrations }}
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

{{/*
aws.connectorsRoleRegistrations is the list of the registrations of the
connectorsRole block, each merged over the defaults of the chart. The
registration aws-connectors-role also gets the permissions boundary as a
limit. It fails on an unknown mode.
*/}}
{{- define "aws.connectorsRoleRegistrations" -}}
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
{{- range .Values.connectorsRole.registrations }}
{{- $registration := deepCopy $defaults }}
{{- if and (eq .name "aws-connectors-role") $.Values.connectorsRole.permissionsBoundaryArn }}
{{- $_ := set $registration "limits" (list (dict "label" "Permissions boundary" "value" $.Values.connectorsRole.permissionsBoundaryArn "description" "The managed policy that bounds the role of the Connectors of each cluster.")) }}
{{- end }}
{{- $out = append $out (mergeOverwrite $registration .) }}
{{- end }}
{{- toYaml $out }}
{{- end -}}

{{/*
aws.kmsRegistrations is the list of the registrations of the kms block, each
with the limit Alias prefix added to its limits.
*/}}
{{- define "aws.kmsRegistrations" -}}
{{- $prefix := dict "label" "Alias prefix" "value" .Values.kms.aliasPrefix "description" "The prefix of the alias of each key, after alias/." }}
{{- $out := list }}
{{- range .Values.kms.registrations }}
{{- $registration := deepCopy . }}
{{- $_ := set $registration "limits" (append (default (list) $registration.limits) $prefix) }}
{{- $out = append $out $registration }}
{{- end }}
{{- toYaml $out }}
{{- end -}}

{{/* aws.ebsRegistrations is the list of the registrations of the ebs block. */}}
{{- define "aws.ebsRegistrations" -}}
{{- toYaml .Values.ebs.registrations }}
{{- end -}}

{{/* aws.s3Registrations is the list of the registrations of the s3 block. */}}
{{- define "aws.s3Registrations" -}}
{{- toYaml .Values.s3.registrations }}
{{- end -}}

{{/*
aws.registrationNames is the resourceNames list of the registrations of one
block: a dict with root, the root context, and block, an item of aws.blocks.
*/}}
{{- define "aws.registrationNames" -}}
{{- range (index .root.Values .block.key).registrations }}
- {{ required "each registration needs a name" .name | quote }}
{{- end }}
{{- end -}}

{{/*
aws.subjects is the ServiceAccount of the release as the subjects of a
binding.
*/}}
{{- define "aws.subjects" -}}
- kind: ServiceAccount
  name: {{ include "aws.fullname" . }}
  namespace: {{ .Release.Namespace }}
{{- end -}}

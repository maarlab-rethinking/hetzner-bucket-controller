{{- define "hetzner-bucket-controller.fullname" -}}
{{- default .Release.Name .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "hetzner-bucket-controller.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "hetzner-bucket-controller.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "hetzner-bucket-controller.selectorLabels" -}}
app.kubernetes.io/name: hetzner-bucket-controller
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "hetzner-bucket-controller.labels" -}}
{{ include "hetzner-bucket-controller.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
{{- end -}}

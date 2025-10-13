{{- define "private-llm-operator.labels" -}}
app.kubernetes.io/name: private-llm
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: Helm
app.kubernetes.io/part-of: private-llm
app.kubernetes.io/version: {{ .Chart.AppVersion }}
{{- end }}



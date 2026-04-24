{{/*
Rewrite a KCP admin kubeconfig so every KCP `server:` field points at the
in-cluster front-proxy under a specific provider workspace path.

Input: an admin kubeconfig whose KCP clusters point at
`https://<external-host>/clusters/<workspace>`.
Output: the same YAML with each KCP server replaced by
`<inClusterServerUrl>/clusters/<providerWorkspace>`.

Usage:
  {{ include "private-llm-msp-app.rewriteKubeconfig" . }}
*/}}
{{- define "private-llm-msp-app.rewriteKubeconfig" -}}
{{- $admin := .Values.kcpKubeconfig.adminContent -}}
{{- $ws := .Values.kcpKubeconfig.providerWorkspace -}}
{{- $target := printf "%s/clusters/%s" .Values.kcpKubeconfig.inClusterServerUrl $ws -}}
{{- regexReplaceAll "server: https://[^[:space:]]+(/clusters/[A-Za-z0-9:_-]+)?" $admin (printf "server: %s" $target) -}}
{{- end -}}

{{/*
Namespace for PublishedResource objects — defaults to the release namespace.
*/}}
{{- define "private-llm-msp-app.syncAgentNamespace" -}}
{{- $ns := (index .Values "private-llm-sync-agent" "publishedResources" "namespace") -}}
{{- if $ns -}}{{ $ns }}{{- else -}}{{ .Release.Namespace }}{{- end -}}
{{- end -}}

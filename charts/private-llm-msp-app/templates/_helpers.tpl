{{/*
Rewrite a KCP admin kubeconfig so the `server:` field points at the in-cluster
front-proxy under a specific provider workspace path.

Input: an admin kubeconfig whose server is `https://<external-host>/clusters/root`.
Output: the same YAML with server replaced by
`<inClusterServerUrl>/clusters/<providerWorkspace>`.

Usage:
  {{ include "private-llm-msp-app.rewriteKubeconfig" . }}
*/}}
{{- define "private-llm-msp-app.rewriteKubeconfig" -}}
{{- $admin := .Values.kcpKubeconfig.adminContent -}}
{{- $ws := .Values.kcpKubeconfig.providerWorkspace -}}
{{- $target := printf "%s/clusters/%s" .Values.kcpKubeconfig.inClusterServerUrl $ws -}}
{{- regexReplaceAll "server: https://[^/\\s]+/clusters/root\\b" $admin (printf "server: %s" $target) -}}
{{- end -}}

{{/*
Namespace for PublishedResource objects — defaults to the release namespace.
*/}}
{{- define "private-llm-msp-app.syncAgentNamespace" -}}
{{- $ns := (index .Values "private-llm-sync-agent" "publishedResources" "namespace") -}}
{{- if $ns -}}{{ $ns }}{{- else -}}{{ .Release.Namespace }}{{- end -}}
{{- end -}}

{{- define "yanet-operator.validateRuntimeResources" -}}
{{- if and (or .Values.yanetconfig .Values.yanets) .Values.webhook.enabled (eq .Values.webhook.failurePolicy "Fail") -}}
  {{- if not .Values.runtimeResources.argoCD -}}
    {{- fail "runtime resources with webhook.failurePolicy=Fail require an ordered Argo CD sync or separate Helm lifecycle" -}}
  {{- end -}}
  {{- if not .Values.webhook.certManager.enabled -}}
    {{- fail "ordered runtime resources with webhook.failurePolicy=Fail require webhook.certManager.enabled" -}}
  {{- end -}}
{{- end -}}
{{- end -}}

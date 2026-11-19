{{- define "yanet-operator.validateRuntimeResources" -}}
{{- if and (or .Values.yanetconfig .Values.yanets) .Values.webhook.enabled (eq .Values.webhook.failurePolicy "Fail") (not .Values.webhook.certManager.enabled) -}}
  {{- fail "runtime resources with webhook.failurePolicy=Fail require webhook.certManager.enabled or a separate lifecycle from the chart's certgen hooks" -}}
{{- end -}}
{{- end -}}

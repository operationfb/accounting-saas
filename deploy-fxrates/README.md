# FX rates — daily refresh (Cloud Scheduler)

The exchange-rate table (`exchange_rates`) is refreshed daily from the ECB (via the
free Frankfurter API). Because Cloud Run scales to zero, an in-process timer can't be
relied on — instead a **Cloud Scheduler** job hits an OIDC-gated internal endpoint,
the same pattern the FreeAgent-push workflow uses.

## Endpoint

```
POST {API_PUBLIC_URL}/internal/v1/fxrates/refresh
```

- Optional `?on=YYYY-MM-DD` query param (defaults to today).
- Gated by `kernel.RequireWorkflowOIDC(WORKFLOW_SERVICE_ACCOUNT)`: the caller must
  present a Google-signed OIDC identity token whose `email` claim equals the
  configured `WORKFLOW_SERVICE_ACCOUNT`. Unset ⇒ the endpoint rejects all calls
  (fails closed).
- Returns `{"refreshed": <n>, "rate_date": "...", "fetched_at": "..."}`.

## Provisioning

Reuse the existing workflow service account (the one in `WORKFLOW_SERVICE_ACCOUNT`)
as the scheduler's invoker identity, so no new IAM principal is needed.

```bash
SA="$WORKFLOW_SERVICE_ACCOUNT"           # e.g. expense-push-wf@PROJECT.iam.gserviceaccount.com
URL="$API_PUBLIC_URL/internal/v1/fxrates/refresh"

gcloud scheduler jobs create http fxrates-daily-refresh \
  --location=europe-west2 \
  --schedule="30 7 * * *" \              # 07:30 daily; ECB publishes ~16:00 CET the prior working day
  --time-zone="Europe/London" \
  --uri="$URL" \
  --http-method=POST \
  --oidc-service-account-email="$SA" \
  --oidc-token-audience="$URL"
```

Notes:
- The endpoint does not pin the OIDC audience (the service-account email IS the
  authorisation), so `--oidc-token-audience` is belt-and-braces.
- A missing rate for a non-trading day is fine — lookups use "on or before", so a
  weekend invoice falls back to Friday's rate. Re-running a day is idempotent
  (`UpsertRate`).
- Local/dev seeds today's rates via a best-effort fetch on startup (no scheduler
  needed); set `FXRATES_PROVIDER_URL=none` to disable the provider entirely.

## Config (env)

| var | purpose | default |
|-----|---------|---------|
| `FXRATES_PROVIDER_URL` | Frankfurter base URL; `none` disables the provider | public host |
| `WORKFLOW_SERVICE_ACCOUNT` | OIDC identity allowed to call the internal refresh | unset ⇒ closed |

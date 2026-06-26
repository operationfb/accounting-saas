# Deploying accounting-saas to Cloud Run (dev)

This folder contains everything needed to containerize the monolith and deploy a
**development** instance to Google Cloud Run in **`europe-west2`** (London — UK data
residency). One Cloud Run service serves **both** the JSON API and the built Vue SPA
from the **same origin**.

Everything deploy-related lives in this `deployment/` folder. The application source
is **not** modified by the deploy (the only app changes are a tiny additive
`/health` route and a `WEB_DIST_DIR`-gated SPA handler), and the project is **not**
copied anywhere — the Docker build simply uses the existing repo as its build
context.

---

## What gets deployed

- **Service:** `accounting-saas-dev` (Cloud Run, `europe-west2`).
- **One origin, two things:**
  - `/api/v1/*` → the Go API (every API route is under `/api/v1`).
  - everything else → the Vue SPA (`web/dist`), with a history-mode fallback to
    `index.html`. Because it's same-origin, the SPA calls the API with a **relative**
    `/api/v1` URL → **no CORS**.
- **Image:** built by **Cloud Build** from [`Dockerfile`](Dockerfile) (3 stages: Node
  builds `web/dist` → Go builds a static binary → tiny `distroless/static` runtime
  holding both), pushed to **Artifact Registry**.
- **Config:** non-secret env from [`env.dev.yaml`](env.dev.yaml.example) (you create
  it); secrets from **Secret Manager**.
- **Identity:** a dedicated least-privilege runtime **service account**; the app
  reaches GCS / Document AI / (future) Pub/Sub through it via Application Default
  Credentials — **no key file, no `GOOGLE_APPLICATION_CREDENTIALS`**.

## How config reaches the container

| Source | Used for | Mechanism |
|--------|----------|-----------|
| Secret Manager | `DATABASE_URL`, `PASETO_SYMMETRIC_KEY`, `MAILGUN_INBOUND_SIGNING_KEY`, `SMTP_PASSWORD` | `--set-secrets` |
| `env.dev.yaml` | all other non-secret env (GCS bucket, Document AI, SMTP host, …) | `--env-vars-file` |
| Dockerfile `ENV` | `WEB_DIST_DIR=/web` | baked into the image |
| Cloud Run | `PORT` (8080) | injected automatically — **do not set it** |
| Attached service account | GCS / Document AI / Pub/Sub auth | ADC via the metadata server |

> The local root `.env` is **never** used in the cloud and is excluded from the
> upload. In particular, `GOOGLE_APPLICATION_CREDENTIALS` (a local key-file path) is
> intentionally **not** set on Cloud Run — ADC falls through to the attached service
> account automatically.

---

## Prerequisites

- The **Google Cloud SDK** (`gcloud`). The local SDK is old (2022) and its stored
  credentials have expired, so step 0 re-authenticates and updates it.
- **No local Docker needed** — the image is built in Cloud Build.
- Run every command **from the repo root** (not from `deployment/`).
- A deploy identity (your `gcloud` user) with rights to enable APIs and create
  Artifact Registry / Cloud Run / Secret Manager / IAM resources in `stocks-ag`.

For convenience, set these once per shell:

```bash
export PROJECT_ID=stocks-ag
export REGION=europe-west1
export REPO=accounting-saas
export SERVICE=accounting-saas-dev
export IMAGE="$REGION-docker.pkg.dev/$PROJECT_ID/$REPO/server"
export RUNTIME_SA="accounting-saas-run@$PROJECT_ID.iam.gserviceaccount.com"
```

---

## One-time setup

### 0. Authenticate, update the SDK, enable APIs

```bash
gcloud auth login                 # interactive (browser) — fixes the expired creds
gcloud components update          # the local SDK is from 2022
gcloud config set project "$PROJECT_ID"

gcloud services enable \
  run.googleapis.com \
  artifactregistry.googleapis.com \
  cloudbuild.googleapis.com \
  secretmanager.googleapis.com \
  documentai.googleapis.com \
  storage.googleapis.com \
  pubsub.googleapis.com
```

### 1. Create the Artifact Registry repo

```bash
gcloud artifacts repositories create "$REPO" \
  --repository-format=docker \
  --location="$REGION" \
  --description="accounting-saas container images"
```

### 2. Create the runtime service account and grant it IAM

A dedicated, least-privilege identity for the running service (not the default
compute SA):

```bash
gcloud iam service-accounts create accounting-saas-run \
  --display-name="accounting-saas Cloud Run runtime"

# Read secrets at runtime (DATABASE_URL, PASETO key, …)
gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="serviceAccount:$RUNTIME_SA" --role="roles/secretmanager.secretAccessor"

# Document AI (OCR / Smart Upload)
gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="serviceAccount:$RUNTIME_SA" --role="roles/documentai.apiUser"

# Pub/Sub publisher — pre-granted for the upcoming FreeAgent "expense-approved"
# event (no Pub/Sub code exists yet; this just avoids a later IAM round-trip)
gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="serviceAccount:$RUNTIME_SA" --role="roles/pubsub.publisher"

# GCS receipt bucket — scoped to the bucket, not project-wide
gcloud storage buckets add-iam-policy-binding gs://accountingtest \
  --member="serviceAccount:$RUNTIME_SA" --role="roles/storage.objectAdmin"

# Let the SA sign V4 download URLs from Cloud Run. There's no private key in-cloud,
# so the storage client signs via the IAM SignBlob API, which requires the SA to be
# a token creator on ITSELF. Without this, attachment "download URL" requests fail.
gcloud iam service-accounts add-iam-policy-binding "$RUNTIME_SA" \
  --member="serviceAccount:$RUNTIME_SA" --role="roles/iam.serviceAccountTokenCreator"
```

> If your bucket name differs from `accountingtest`, change it above (and in
> `env.dev.yaml`). On the old SDK, the bucket binding alternative is:
> `gsutil iam ch serviceAccount:$RUNTIME_SA:roles/storage.objectAdmin gs://accountingtest`.

### 3. Create the secrets (piped from `.env`, never typed)

This reads each value straight from the root `.env` and strips the trailing newline,
so no secret is ever typed on the command line or committed:

```bash
for kv in DATABASE_URL:database-url \
          PASETO_SYMMETRIC_KEY:paseto-symmetric-key \
          MAILGUN_INBOUND_SIGNING_KEY:mailgun-inbound-signing-key \
          SMTP_PASSWORD:smtp-password; do
  envkey="${kv%%:*}"; secret="${kv##*:}"
  grep -E "^${envkey}=" .env | cut -d= -f2- | tr -d '\n' \
    | gcloud secrets create "$secret" --data-file=- --replication-policy=automatic
done
```

To rotate a secret later, add a new version:
`grep -E '^DATABASE_URL=' .env | cut -d= -f2- | tr -d '\n' | gcloud secrets versions add database-url --data-file=-`.

### 4. Create your non-secret env file

```bash
cp deployment/env.dev.yaml.example deployment/env.dev.yaml
# then edit deployment/env.dev.yaml, filling each value from your root .env
```

`deployment/env.dev.yaml` is gitignored (via `deployment/.gitignore`).

---

## Build & deploy

### 5. Build and push the image (Cloud Build)

```bash
gcloud builds submit . \
  --config deployment/cloudbuild.yaml \
  --ignore-file deployment/.gcloudignore \
  --substitutions _TAG=$(git rev-parse --short HEAD)
```

This uploads the repo (minus everything in `.gcloudignore`), builds the 3-stage
image, and pushes `:latest` plus a `:<short-sha>` tag to Artifact Registry.

> If the push is denied, grant the Cloud Build service account writer access once:
> ```bash
> CB_SA="$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)')@cloudbuild.gserviceaccount.com"
> gcloud projects add-iam-policy-binding "$PROJECT_ID" \
>   --member="serviceAccount:$CB_SA" --role="roles/artifactregistry.writer"
> ```

### 6. Deploy to Cloud Run

```bash
gcloud run deploy "$SERVICE" \
  --image="$IMAGE:latest" \
  --region="$REGION" --platform=managed \
  --service-account="$RUNTIME_SA" \
  --allow-unauthenticated --port=8080 \
  --cpu=1 --memory=512Mi --min-instances=0 --max-instances=2 \
  --env-vars-file=deployment/env.dev.yaml \
  --set-secrets="DATABASE_URL=database-url:latest,PASETO_SYMMETRIC_KEY=paseto-symmetric-key:latest,MAILGUN_INBOUND_SIGNING_KEY=mailgun-inbound-signing-key:latest,SMTP_PASSWORD=smtp-password:latest"
```

### 7. Point `APP_BASE_URL` at the deployed URL

`APP_BASE_URL` builds password-reset links (and, later, OAuth redirect URIs). Since
the SPA and API share this origin, the reset link `{APP_BASE_URL}/reset-password/…`
resolves to a real SPA route. Set it after the first deploy:

```bash
URL=$(gcloud run services describe "$SERVICE" --region="$REGION" --format='value(status.url)')
gcloud run services update "$SERVICE" --region="$REGION" --update-env-vars="APP_BASE_URL=https://app.kontala.com"
echo "Deployed at: $URL"
```
gcloud run services update "accounting-saas-dev" --region="europe-west1" --update-env-vars="HMRC_SANDBOX=true"
---

## Verify

```bash
URL=$(gcloud run services describe "$SERVICE" --region="$REGION" --format='value(status.url)')

# 1. Startup logs — the key check. Expect:
#    "database connection established", "serving SPA from /web", "server listening on :8080"
gcloud run services logs read "$SERVICE" --region="$REGION" --limit=50

# 2. Liveness (public, no auth, no DB). Path is /health — GCP RESERVES /healthz (it
#    404s at Google's front end before reaching the container), so we use /health.
curl -fsS "$URL/health"                        # -> {"status":"ok"}

# 3. SPA is served (index.html), incl. a client-side route
curl -fsS "$URL/"      | grep -i '<div id="app"'
curl -fsS "$URL/login" | grep -i '<div id="app"'   # history-mode fallback

# 4. API is NOT shadowed by the SPA fallback
curl -s -o /dev/null -w '%{http_code}\n' "$URL/api/v1/does-not-exist"   # -> 404 (JSON)

# 5. End-to-end DB + auth (dev seed user)
curl -s -X POST "$URL/api/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"dev@example.com","password":"devpassword123"}'          # -> 200 + token
```

Then open `$URL` in a browser, log in with `dev@example.com` / `devpassword123`, and
confirm the app works (its `/api/v1` calls are same-origin, so no CORS).

---

## Redeploying a new version

Repeat **steps 5 and 6** (build → deploy). The one-time setup (0–4) and step 7
(`APP_BASE_URL`) don't need repeating unless the service URL changes.

---

## Troubleshooting

- **`database ping failed` in the logs (container won't start).** The app pings
  Postgres at startup. The dev DB is a public-IP Postgres reached over the internet;
  Cloud Run egresses from dynamic IPs. If the DB only allows specific IPs, the ping
  fails. Give Cloud Run a **stable egress IP** and allowlist it on the DB:
  create a Serverless VPC connector (or use Direct VPC egress) + Cloud NAT with a
  reserved static IP, redeploy with `--vpc-egress=all-traffic --vpc-connector=<name>`,
  then add that IP to the DB's authorized networks. (Often unnecessary — a dev DB you
  reach from a laptop is frequently already open to `0.0.0.0/0`.)
- **Build push denied.** See the Cloud Build SA grant under step 5.
- **`gcloud crashed` / `invalid_grant`.** Stale credentials or SDK — rerun
  `gcloud auth login` and `gcloud components update` (step 0).
- **Attachment download URLs fail (signing error).** Ensure the
  `roles/iam.serviceAccountTokenCreator` self-binding from step 2 is in place.

## Notes & out of scope

- **`sslmode=disable`** on the dev `DATABASE_URL` is over the public internet —
  acceptable for dev, but harden before production (Cloud SQL connector or
  `verify-full`).
- This is the generic containerize + deploy foundation only. The FreeAgent
  integration, the `/internal/v1/...` OIDC endpoints, Eventarc/Workflows, and any
  Pub/Sub *code* are out of scope here (only the `pubsub.publisher` IAM role is
  pre-granted).

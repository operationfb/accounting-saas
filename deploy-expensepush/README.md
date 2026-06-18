# Deploy: FreeAgent expense push (GCP-native)

How the "push approved expenses to FreeAgent" integration is wired in GCP, and how
to provision it. Everything runs in **`europe-west1`**, co-located with the
monolith's Cloud Run service.

> **Data-residency note:** `europe-west1` is Belgium (EU), not the UK. Lawful for
> UK personal data under UK–EU adequacy, but EU- not UK-residency. `CLAUDE.md`
> still cites `europe-west2` for UK residency — reconcile separately.

## Flow

```
approve expense ─▶ monolith publishes {organisation_id, expense_id}
                   to Pub/Sub topic `expense-approved`
                          │
                    Eventarc trigger
                          ▼
                 Cloud Workflow `freeagent-push`  (deploy/workflows/freeagent-push.yaml)
                          │  (OIDC service-account calls)
        ┌─────────────────┴─────────────────────────────────────────┐
        ▼                                                            ▼
  monolith /internal/v1 endpoints                         FreeAgent API
  (token, expense data, push-result)                      (GET /users, POST /expenses)
```

The **field mapping + the FreeAgent API calls live in the workflow YAML**, not the
monolith. The monolith only: publishes the event, vends a token, serves the
expense data (money already as decimal strings), and records the outcome.

## Monolith environment variables

| Var | Purpose | Example |
|---|---|---|
| `PUBSUB_EXPENSE_APPROVED_TOPIC` | Topic to publish approvals to (enables publishing) | `expense-approved` |
| `GOOGLE_CLOUD_PROJECT` | Project for the Pub/Sub client (optional — auto-detected from ADC) | `my-project` |
| `WORKFLOW_SERVICE_ACCOUNT` | The workflow's SA email; `/internal/v1` accepts only its OIDC tokens | `freeagent-push-wf@my-project.iam.gserviceaccount.com` |
| `API_PUBLIC_URL` | The monolith's own public base URL; builds the OAuth `redirect_uri` | `https://api.example.com` |
| `FREEAGENT_SANDBOX` | `true` → talk to FreeAgent's sandbox host | `true` |

All are optional: with `PUBSUB_EXPENSE_APPROVED_TOPIC` unset the monolith doesn't
publish; with `WORKFLOW_SERVICE_ACCOUNT` unset the `/internal/v1` endpoints fail
closed. The OAuth connect endpoints are always mounted.

## FreeAgent app setup

In the FreeAgent (sandbox) developer dashboard, register an OAuth app and set its
**redirect URI** to `${API_PUBLIC_URL}/api/v1/freeagent/callback`. The org admin
enters that app's client ID + secret on the in-product settings screen (they are
stored per-org; not server env vars).

## Provision (one-off, `gcloud`)

```bash
PROJECT=your-project-id
REGION=europe-west1
MONOLITH_SERVICE=accounting-saas                 # the Cloud Run service name
MONOLITH_SA=...                                  # the Cloud Run runtime SA (publishes events)
WF_SA="freeagent-push-wf@${PROJECT}.iam.gserviceaccount.com"

# 1. Pub/Sub: the event topic + a dead-letter topic for poison messages.
gcloud pubsub topics create expense-approved     --project=$PROJECT --message-storage-policy-allowed-regions=$REGION
gcloud pubsub topics create expense-approved-dlq --project=$PROJECT --message-storage-policy-allowed-regions=$REGION

# 2. The monolith's runtime SA must be allowed to publish.
gcloud pubsub topics add-iam-policy-binding expense-approved --project=$PROJECT \
    --member="serviceAccount:${MONOLITH_SA}" --role="roles/pubsub.publisher"

# 3. The workflow's service account + the roles it needs.
gcloud iam service-accounts create freeagent-push-wf --project=$PROJECT \
    --display-name="FreeAgent push workflow"
# Call the monolith's OIDC-gated /internal endpoints (Cloud Run):
gcloud run services add-iam-policy-binding $MONOLITH_SERVICE --project=$PROJECT --region=$REGION \
    --member="serviceAccount:${WF_SA}" --role="roles/run.invoker"
# Be started by Eventarc, and run as itself:
gcloud projects add-iam-policy-binding $PROJECT --member="serviceAccount:${WF_SA}" --role="roles/workflows.invoker"
gcloud projects add-iam-policy-binding $PROJECT --member="serviceAccount:${WF_SA}" --role="roles/eventarc.eventReceiver"

# 4. Deploy the workflow.  FIRST edit deploy/workflows/freeagent-push.yaml and set
#    MONOLITH to the deployed Cloud Run URL.
gcloud workflows deploy freeagent-push --project=$PROJECT --location=$REGION \
    --source=deploy/workflows/freeagent-push.yaml \
    --service-account="${WF_SA}"

# 5. Eventarc: Pub/Sub topic → workflow.
gcloud eventarc triggers create freeagent-push-trigger --project=$PROJECT --location=$REGION \
    --destination-workflow=freeagent-push \
    --destination-workflow-location=$REGION \
    --event-filters="type=google.cloud.pubsub.topic.v1.messagePublished" \
    --transport-topic="projects/${PROJECT}/topics/expense-approved" \
    --service-account="${WF_SA}"

# 6. Set the monolith env (Cloud Run): PUBSUB_EXPENSE_APPROVED_TOPIC=expense-approved,
#    GOOGLE_CLOUD_PROJECT=$PROJECT, WORKFLOW_SERVICE_ACCOUNT=${WF_SA}, then redeploy.
```

> **Dead-letter:** Eventarc manages its own subscription on `expense-approved`.
> To stop a poison message retrying forever, attach a dead-letter policy pointing
> at `expense-approved-dlq` to that subscription (Console → the Eventarc-created
> subscription → Dead lettering), and grant Pub/Sub's service agent publish rights
> on the DLQ. This is a follow-up, not required for the happy path.

## Verify end to end (needs the deployed monolith — see the separate Cloud Run task)

1. Connect a FreeAgent **sandbox** org via the in-product settings → Connect flow.
2. Approve an expense (`POST /api/v1/expenses/:id/status` `{"action":"approve"}`).
3. The monolith publishes; Eventarc starts `freeagent-push`. Check the execution in
   the Workflows console (or `gcloud workflows executions list`).
4. Confirm the expense appears in the FreeAgent sandbox under the right claimant +
   category, with a **negated** gross value.
5. Confirm the monolith stored the outcome:
   `SELECT external_expense_ref FROM integration_expense_pushes WHERE expense_id = '…';`
6. **Idempotency:** re-push (`POST /api/v1/integrations/freeagent/expenses/:id/push`)
   → the workflow exits at `already_pushed`, no duplicate in FreeAgent.

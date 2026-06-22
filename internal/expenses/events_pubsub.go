package expenses

// events_pubsub.go
// =============================================================================
// pubsubPublisher — the Google Cloud Pub/Sub implementation of EventPublisher.
//
// It publishes "expense.approved" to a Pub/Sub topic in our own GCP region; an
// Eventarc trigger on that topic starts the FreeAgent push workflow. We use the
// v2 client (the v1 package is deprecated). Credentials come from Application
// Default Credentials (locally: `gcloud auth application-default login`; on Cloud
// Run: the attached service account) — exactly like the GCS storage client.
// =============================================================================

import (
	"context"
	"encoding/json"
	"fmt"

	pubsub "cloud.google.com/go/pubsub/v2"
)

// pubsubPublisher holds the Pub/Sub client and a Publisher bound to one topic.
type pubsubPublisher struct {
	client    *pubsub.Client
	publisher *pubsub.Publisher
}

// NewPubSubPublisher builds a publisher for topicID in projectID. An empty
// projectID is auto-detected from ADC (so callers can pass GOOGLE_CLOUD_PROJECT
// verbatim). The topic must already exist (created out-of-band — see the plan's
// infra section).
func NewPubSubPublisher(ctx context.Context, projectID, topicID string) (*pubsubPublisher, error) {
	if projectID == "" {
		projectID = pubsub.DetectProjectID
	}
	client, err := pubsub.NewClient(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("pubsub.NewClient: %w", err)
	}
	return &pubsubPublisher{
		client:    client,
		publisher: client.Publisher(topicID),
	}, nil
}

// PublishExpenseApproved publishes the event as JSON, with the event type also set
// as a message attribute. It blocks on result.Get so a publish failure is returned
// to the caller (who logs it best-effort) rather than swallowed in the async batch.
func (p *pubsubPublisher) PublishExpenseApproved(ctx context.Context, e ExpenseApprovedEvent) error {
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal expense.approved event: %w", err)
	}
	result := p.publisher.Publish(ctx, &pubsub.Message{
		Data:       data,
		Attributes: map[string]string{"event_type": e.Event},
	})
	if _, err := result.Get(ctx); err != nil {
		return fmt.Errorf("publish expense.approved: %w", err)
	}
	return nil
}

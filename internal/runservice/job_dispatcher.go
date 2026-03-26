package runservice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"golang.org/x/oauth2/google"
)

const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// CloudRunJobDispatcher launches a Cloud Run Job execution for each queued run.
type CloudRunJobDispatcher struct {
	client        *http.Client
	jobResource   string
	containerName string
}

// NewCloudRunJobDispatcher constructs a dispatcher that triggers Cloud Run Job
// executions using application default credentials.
func NewCloudRunJobDispatcher(
	ctx context.Context,
	projectID string,
	region string,
	jobName string,
	containerName string,
) (*CloudRunJobDispatcher, error) {
	projectID = strings.TrimSpace(projectID)
	region = strings.TrimSpace(region)
	jobName = strings.TrimSpace(jobName)
	if projectID == "" || region == "" || jobName == "" {
		return nil, fmt.Errorf("project ID, region, and job name are required")
	}

	client, err := google.DefaultClient(ctx, cloudPlatformScope)
	if err != nil {
		return nil, fmt.Errorf("create Cloud Run job client: %w", err)
	}

	return &CloudRunJobDispatcher{
		client:        client,
		jobResource:   fmt.Sprintf("projects/%s/locations/%s/jobs/%s", projectID, region, jobName),
		containerName: strings.TrimSpace(containerName),
	}, nil
}

// Enqueue triggers a new Cloud Run Job execution for the provided run ID.
func (d *CloudRunJobDispatcher) Enqueue(ctx context.Context, runID string) error {
	payload := runJobRequest{
		Overrides: runJobOverrides{
			ContainerOverrides: []runJobContainerOverride{
				{
					Name: d.containerName,
					Env: []runJobEnvVar{
						{
							Name:  "ASSET_DISCOVERY_RUN_ID",
							Value: strings.TrimSpace(runID),
						},
					},
				},
			},
		},
	}
	if d.containerName == "" {
		payload.Overrides.ContainerOverrides[0].Name = ""
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal run job request: %w", err)
	}

	url := fmt.Sprintf("https://run.googleapis.com/v2/%s:run", d.jobResource)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build Cloud Run job request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := d.client.Do(request)
	if err != nil {
		return fmt.Errorf("run Cloud Run job: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		rawBody, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("run Cloud Run job: %s: %s", response.Status, strings.TrimSpace(string(rawBody)))
	}

	return nil
}

type runJobRequest struct {
	Overrides runJobOverrides `json:"overrides"`
}

type runJobOverrides struct {
	ContainerOverrides []runJobContainerOverride `json:"containerOverrides"`
}

type runJobContainerOverride struct {
	Name string         `json:"name,omitempty"`
	Env  []runJobEnvVar `json:"env,omitempty"`
}

type runJobEnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"dagger.io/dagger"
	"github.com/google/go-github/v48/github"
	"github.com/jlongtine/dagger-experiments/actionsrunner"
	"github.com/palantir/go-githubapp/githubapp"
	"golang.org/x/oauth2"
)

func main() {
	ctx := context.Background()

	paToken, ok := os.LookupEnv("GHA_RUNNER_TOKEN")
	if !ok {
		panic(fmt.Errorf("GHA_RUNNER_TOKEN is not set"))
	}
	repo, ok := os.LookupEnv("GHA_RUNNER_REPO")
	if !ok {
		panic(fmt.Errorf("GHA_RUNNER_REPO is not set"))
	}

	labels := []string{"dagger-runner"}
	if envLabels, ok := os.LookupEnv("GHA_RUNNER_LABELS"); ok {
		labels = strings.Split(envLabels, ",")
	}

	runnerPrefix := "test-dagger-runner"
	if envRunnerPrefix, ok := os.LookupEnv("GHA_RUNNER_RUNNER_PREFIX"); ok {
		runnerPrefix = envRunnerPrefix
	}

	appID, err := strconv.Atoi(os.Getenv("GH_APP_ID"))
	if err != nil {
		panic(err)
	}

	webhookSecret, ok := os.LookupEnv("GH_WEBHOOK_SECRET")
	if !ok {
		panic(fmt.Errorf("GH_WEBHOOK_SECRET is not set"))
	}

	privateKey, ok := os.LookupEnv("GH_PRIVATE_KEY")
	if !ok {
		panic(fmt.Errorf("GH_PRIVATE_KEY is not set"))
	}

	cfg := githubapp.Config{
		V3APIURL: "https://api.github.com/",
		App: struct {
			IntegrationID int64  `yaml:"integration_id" json:"integrationId"`
			WebhookSecret string `yaml:"webhook_secret" json:"webhookSecret"`
			PrivateKey    string `yaml:"private_key" json:"privateKey"`
		}{
			IntegrationID: int64(appID),
			WebhookSecret: webhookSecret,
			PrivateKey:    privateKey,
		},
	}

	c, err := dagger.Connect(ctx, dagger.WithLogOutput(os.Stderr))
	if err != nil {
		panic(err)
	}
	defer c.Close()

	h := &handler{
		daggerClient: c,
		paToken:      paToken,
		repo:         repo,
		labels:       labels,
		runnerPrefix: runnerPrefix,
	}

	http.Handle("/", githubapp.NewDefaultEventDispatcher(cfg, h))

	addr := fmt.Sprintf("%s:%d", "0.0.0.0", 45363)
	err = http.ListenAndServe(addr, nil)
	if err != nil {
		panic(err)
	}
}

type handler struct {
	daggerClient *dagger.Client
	paToken      string
	repo         string
	labels       []string
	runnerPrefix string
}

func (h *handler) Handles() []string {
	return []string{"workflow_job"}
}

func (h *handler) Handle(ctx context.Context, eventType, deliveryID string, payload []byte) error {
	fmt.Println("Received event", eventType)
	if eventType != "workflow_job" {
		return nil
	}
	var event github.WorkflowJobEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return err
	}
	if event.GetAction() != "queued" {
		return nil
	}
	runnerLabels := event.WorkflowJob.Labels
	if len(runnerLabels) == 0 {
		return nil
	}
	var isDaggerRunner bool
	for _, label := range runnerLabels {
		// n^2 but who cares
		for _, otherLabel := range h.labels {
			if label == otherLabel {
				isDaggerRunner = true
				break
			}
		}
	}
	if !isDaggerRunner {
		fmt.Printf("received job for different runner %v, ignoring\n", runnerLabels)
		return nil
	}

	// setup an http client that uses the personal access token
	// to authenticate to github using oauth
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: h.paToken})
	tc := oauth2.NewClient(ctx, ts)
	ghClient := github.NewClient(tc)

	// obtain the owner and repo from the repo url
	u, err := url.Parse(h.repo)
	if err != nil {
		return err
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) != 3 {
		return fmt.Errorf("invalid repo url: %s", h.repo)
	}
	owner := parts[1]
	repo := parts[2]
	runnerToken, resp, err := ghClient.Actions.CreateRegistrationToken(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("failed to create runner token: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	go func() {
		fmt.Println("starting actions runner", eventType)
		err := actionsrunner.Run(context.TODO(), h.daggerClient, actionsrunner.Config{
			Token:            *runnerToken.Token,
			Repo:             h.repo,
			Labels:           h.labels,
			RunnerNamePrefix: h.runnerPrefix,
			Count:            1,
		})
		if err != nil {
			fmt.Printf("runner error: %v\n", err)
		}
	}()

	return nil
}

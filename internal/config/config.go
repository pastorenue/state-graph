package config

import (
	"errors"
	"fmt"
	"log"
	"os"
)

// Config holds all runtime configuration loaded from environment variables.
type Config struct {
	// MongoURI is the MongoDB connection URI. Required.
	// Source: KFLOW_MONGO_URI
	MongoURI string

	// MongoDB is the database name. Defaults to "kflow".
	// Source: KFLOW_MONGO_DB
	MongoDB string

	// Namespace is the Kubernetes namespace used for all workloads.
	// Defaults to "kflow".
	// Source: KFLOW_NAMESPACE
	Namespace string

	// RunnerGRPCEndpoint is the internal address of the RunnerService.
	// Injected into K8s Job containers as KFLOW_GRPC_ENDPOINT.
	// Defaults to "kflow-cp.kflow.svc.cluster.local:9090".
	// Source: KFLOW_RUNNER_GRPC_ENDPOINT
	RunnerGRPCEndpoint string

	// RunnerTokenSecret is the HMAC-SHA256 key used to sign state tokens.
	// Required in production. Min 32 bytes.
	// Source: KFLOW_RUNNER_TOKEN_SECRET
	RunnerTokenSecret string

	// APIKey is the Bearer token for API authentication.
	// Empty means auth is disabled (dev mode only).
	// Source: KFLOW_API_KEY
	APIKey string

	// ClickHouseDSN is the ClickHouse connection string.
	// If empty, telemetry is disabled (no-op mode).
	// Source: KFLOW_CLICKHOUSE_DSN
	ClickHouseDSN string

	// ObjectStoreURI is the S3-compatible URI for large output offload.
	// If empty, outputs > 1 MB return ErrOutputTooLarge.
	// Source: KFLOW_OBJECT_STORE_URI
	ObjectStoreURI string
}

// LoadConfig reads configuration from environment variables.
// Returns an error if any required variable is missing.
func LoadConfig() (*Config, error) {
	mongoURI := os.Getenv("KFLOW_MONGO_URI")
	if mongoURI == "" {
		return nil, errors.New("config: KFLOW_MONGO_URI is required but not set")
	}

	mongoDB := os.Getenv("KFLOW_MONGO_DB")
	if mongoDB == "" {
		mongoDB = "kflow"
	}

	namespace := os.Getenv("KFLOW_NAMESPACE")
	if namespace == "" {
		namespace = "kflow"
	}

	runnerEndpoint := os.Getenv("KFLOW_RUNNER_GRPC_ENDPOINT")
	if runnerEndpoint == "" {
		runnerEndpoint = "kflow-cp.kflow.svc.cluster.local:9090"
	}

	tokenSecret := os.Getenv("KFLOW_RUNNER_TOKEN_SECRET")
	if len(tokenSecret) < 32 {
		log.Println("config: WARNING KFLOW_RUNNER_TOKEN_SECRET is unset or too short — state token security is disabled (dev mode only)")
	}

	apiKey := os.Getenv("KFLOW_API_KEY")
	if apiKey == "" {
		log.Println("config: WARNING KFLOW_API_KEY is not set — API authentication is disabled (dev mode only)")
	}

	if err := validateMongoURI(mongoURI); err != nil {
		return nil, err
	}

	clickhouseDSN := os.Getenv("KFLOW_CLICKHOUSE_DSN")
	objectStoreURI := os.Getenv("KFLOW_OBJECT_STORE_URI")

	return &Config{
		MongoURI:           mongoURI,
		MongoDB:            mongoDB,
		Namespace:          namespace,
		RunnerGRPCEndpoint: runnerEndpoint,
		RunnerTokenSecret:  tokenSecret,
		APIKey:             apiKey,
		ClickHouseDSN:      clickhouseDSN,
		ObjectStoreURI:     objectStoreURI,
	}, nil
}

func validateMongoURI(uri string) error {
	if len(uri) < 10 {
		return fmt.Errorf("config: KFLOW_MONGO_URI appears invalid: %q", uri)
	}
	return nil
}

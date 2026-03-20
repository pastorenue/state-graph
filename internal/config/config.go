package config

import (
	"errors"
	"fmt"
	"log"
	"os"
)

// Config holds all runtime configuration loaded from environment variables.
type Config struct {
	MongoURI  string
	MongoDB   string
	Namespace string

	GRPCPort       string // KFLOW_GRPC_PORT, default "8080"
	RunnerGRPCPort string // KFLOW_RUNNER_GRPC_PORT, default "9090"
	GRPCTLSCert    string // KFLOW_GRPC_TLS_CERT
	GRPCTLSKey     string // KFLOW_GRPC_TLS_KEY

	RunnerGRPCEndpoint string // KFLOW_RUNNER_GRPC_ENDPOINT
	RunnerTokenSecret  []byte // KFLOW_RUNNER_TOKEN_SECRET

	ServiceGRPCPort string // KFLOW_SERVICE_GRPC_PORT, default "9091"

	APIKey         string
	ClickHouseDSN  string
	ObjectStoreURI string // KFLOW_OBJECT_STORE_URI: S3-compatible URI; empty = large outputs return ErrOutputTooLarge
	Image          string // KFLOW_IMAGE: container image for K8s Job execution (optional)
}

// LoadConfig reads configuration from environment variables.
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

	grpcPort := os.Getenv("KFLOW_GRPC_PORT")
	if grpcPort == "" {
		grpcPort = "8080"
	}

	runnerGRPCPort := os.Getenv("KFLOW_RUNNER_GRPC_PORT")
	if runnerGRPCPort == "" {
		runnerGRPCPort = "9090"
	}

	serviceGRPCPort := os.Getenv("KFLOW_SERVICE_GRPC_PORT")
	if serviceGRPCPort == "" {
		serviceGRPCPort = "9091"
	}

	runnerEndpoint := os.Getenv("KFLOW_RUNNER_GRPC_ENDPOINT")
	if runnerEndpoint == "" {
		runnerEndpoint = "kflow-cp.kflow.svc.cluster.local:9090"
	}

	tokenSecretStr := os.Getenv("KFLOW_RUNNER_TOKEN_SECRET")
	tokenSecret := []byte(tokenSecretStr)
	if len(tokenSecret) == 0 {
		log.Println("config: WARNING KFLOW_RUNNER_TOKEN_SECRET is not set — state token security is disabled (dev mode only)")
	} else if len(tokenSecret) < 32 {
		return nil, fmt.Errorf("config: KFLOW_RUNNER_TOKEN_SECRET must be at least 32 bytes, got %d", len(tokenSecret))
	}

	apiKey := os.Getenv("KFLOW_API_KEY")
	if apiKey == "" {
		log.Println("config: WARNING KFLOW_API_KEY is not set — API authentication is disabled (dev mode only)")
	}

	if err := validateMongoURI(mongoURI); err != nil {
		return nil, err
	}

	return &Config{
		MongoURI:           mongoURI,
		MongoDB:            mongoDB,
		Namespace:          namespace,
		GRPCPort:           grpcPort,
		RunnerGRPCPort:     runnerGRPCPort,
		GRPCTLSCert:        os.Getenv("KFLOW_GRPC_TLS_CERT"),
		GRPCTLSKey:         os.Getenv("KFLOW_GRPC_TLS_KEY"),
		RunnerGRPCEndpoint: runnerEndpoint,
		RunnerTokenSecret:  tokenSecret,
		ServiceGRPCPort:    serviceGRPCPort,
		APIKey:             apiKey,
		ClickHouseDSN:      os.Getenv("KFLOW_CLICKHOUSE_DSN"),
		ObjectStoreURI:     os.Getenv("KFLOW_OBJECT_STORE_URI"),
		Image:              os.Getenv("KFLOW_IMAGE"),
	}, nil
}

func validateMongoURI(uri string) error {
	if len(uri) < 10 {
		return fmt.Errorf("config: KFLOW_MONGO_URI appears invalid: %q", uri)
	}
	return nil
}

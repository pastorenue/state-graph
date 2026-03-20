package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

var ErrObjectNotFound = errors.New("object store: key not found")

// ObjectStore wraps an S3-compatible object store for large output offload.
type ObjectStore struct {
	client     *s3.Client
	bucketName string
}

// NewObjectStore parses a "s3://<bucket>" URI and returns a connected ObjectStore.
func NewObjectStore(ctx context.Context, uri string) (*ObjectStore, error) {
	if !strings.HasPrefix(uri, "s3://") {
		return nil, fmt.Errorf("object store: URI must start with s3://, got %q", uri)
	}
	bucket := strings.TrimPrefix(uri, "s3://")
	bucket = strings.TrimRight(bucket, "/")
	if bucket == "" {
		return nil, fmt.Errorf("object store: bucket name is empty in URI %q", uri)
	}

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("object store: load AWS config: %w", err)
	}

	return &ObjectStore{
		client:     s3.NewFromConfig(cfg),
		bucketName: bucket,
	}, nil
}

// Put uploads data to the given key in the bucket.
func (s *ObjectStore) Put(ctx context.Context, key string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("object store: put %q: %w", key, err)
	}
	return nil
}

// Get downloads data from the given key in the bucket.
// Returns ErrObjectNotFound if the key does not exist.
func (s *ObjectStore) Get(ctx context.Context, key string) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(key),
	})
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, ErrObjectNotFound
		}
		return nil, fmt.Errorf("object store: get %q: %w", key, err)
	}
	defer out.Body.Close()

	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 4096)
	for {
		n, readErr := out.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if readErr != nil {
			break
		}
	}
	return buf, nil
}

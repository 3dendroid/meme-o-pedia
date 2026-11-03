package main

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// ImageStore keeps meme pictures in S3-compatible storage (SeaweedFS in the cluster).
// The interface lets tests replace S3 with a fake.
type ImageStore interface {
	Put(ctx context.Context, key, contentType string, data []byte) error
	Get(ctx context.Context, key string) (io.ReadCloser, string, error)
}

var ErrNoImages = errors.New("image storage is not configured")

type s3Images struct {
	client *s3.Client
	bucket string
}

func newS3Images(endpoint, bucket, accessKey, secretKey string) *s3Images {
	client := s3.New(s3.Options{
		Region:       "us-east-1", // required by the SDK; SeaweedFS ignores it
		BaseEndpoint: aws.String(endpoint),
		UsePathStyle: true, // http://seaweedfs:8333/bucket/key instead of http://bucket.seaweedfs:8333/key
		Credentials:  credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		// Newer SDKs add checksums that not every S3-compatible server understands.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &s3Images{client: client, bucket: bucket}
}

// ensureBucket creates the bucket on the first upload. Uploads are rare,
// so one extra request per upload is fine, and the API does not depend
// on storage being up at startup.
func (s *s3Images) ensureBucket(ctx context.Context) error {
	if _, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &s.bucket}); err == nil {
		return nil
	}
	_, err := s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &s.bucket})
	var owned *types.BucketAlreadyOwnedByYou
	if errors.As(err, &owned) {
		return nil
	}
	return err
}

func (s *s3Images) Put(ctx context.Context, key, contentType string, data []byte) error {
	if err := s.ensureBucket(ctx); err != nil {
		return err
	}
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        &s.bucket,
		Key:           &key,
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
		ContentType:   &contentType,
	})
	return err
}

func (s *s3Images) Get(ctx context.Context, key string) (io.ReadCloser, string, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		var missing *types.NoSuchKey
		if errors.As(err, &missing) {
			return nil, "", ErrNotFound
		}
		return nil, "", err
	}
	return out.Body, aws.ToString(out.ContentType), nil
}

// noImages is used when S3 is not configured: the site works, uploads are refused.
type noImages struct{}

func (noImages) Put(context.Context, string, string, []byte) error { return ErrNoImages }
func (noImages) Get(context.Context, string) (io.ReadCloser, string, error) {
	return nil, "", ErrNotFound
}

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"google.golang.org/api/iterator"
)

type objectStore struct {
	settings settings
	s3       *s3.Client
	transfer *transfermanager.Client
	gcs      *storage.Client
}

func newObjectStore(s settings) (*objectStore, error) {
	store := &objectStore{settings: s}
	switch s.provider {
	case "", "s3", "r2":
		if s.provider == "s3" || s.provider == "r2" {
			region := env("AWS_DEFAULT_REGION", "")
			if region == "" {
				region = env("AWS_REGION", "")
			}
			if region == "" {
				region = "us-east-1"
				if s.provider == "r2" {
					region = "auto"
				}
			}
			options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
			cfg, err := awsconfig.LoadDefaultConfig(context.Background(), options...)
			if err != nil {
				return nil, fmt.Errorf("load AWS configuration: %w", err)
			}
			store.s3 = s3.NewFromConfig(cfg, func(options *s3.Options) {
				if s.provider == "r2" && s.endpointURL != "" {
					options.BaseEndpoint = aws.String(s.endpointURL)
					options.UsePathStyle = true
				}
			})
			store.transfer = transfermanager.New(store.s3)
		}
	case "gcs":
		client, err := storage.NewClient(context.Background())
		if err != nil {
			return nil, fmt.Errorf("create Google Cloud Storage client: %w", err)
		}
		store.gcs = client
	default:
		return nil, fmt.Errorf("unsupported storage provider %q", s.provider)
	}
	return store, nil
}

func (s *objectStore) upload(source, key string) error {
	if s.settings.provider == "" {
		return nil
	}
	objectKey := s.objectKey(key)
	switch s.settings.provider {
	case "s3", "r2":
		file, err := os.Open(source)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = s.transfer.UploadObject(context.Background(), &transfermanager.UploadObjectInput{
			Bucket: aws.String(s.settings.bucket), Key: aws.String(objectKey), Body: file,
		})
		return err
	case "gcs":
		file, err := os.Open(source)
		if err != nil {
			return err
		}
		defer file.Close()
		writer := s.gcs.Bucket(s.settings.bucket).Object(objectKey).NewWriter(context.Background())
		if _, err := io.Copy(writer, file); err != nil {
			_ = writer.Close()
			return err
		}
		return writer.Close()
	default:
		return fmt.Errorf("unsupported storage provider %q", s.settings.provider)
	}
}

func (s *objectStore) download(key, destination string) (bool, error) {
	if s.settings.provider == "" {
		return false, nil
	}
	objectKey := s.objectKey(key)
	switch s.settings.provider {
	case "s3", "r2":
		output, err := s.s3.GetObject(context.Background(), &s3.GetObjectInput{
			Bucket: aws.String(s.settings.bucket), Key: aws.String(objectKey),
		})
		if err != nil {
			var apiError smithy.APIError
			if errors.As(err, &apiError) && (apiError.ErrorCode() == "404" || apiError.ErrorCode() == "NoSuchKey" || apiError.ErrorCode() == "NotFound") {
				return false, nil
			}
			return false, err
		}
		defer output.Body.Close()
		return writeDownloadedFile(destination, output.Body)
	case "gcs":
		reader, err := s.gcs.Bucket(s.settings.bucket).Object(objectKey).NewReader(context.Background())
		if errors.Is(err, storage.ErrObjectNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		defer reader.Close()
		return writeDownloadedFile(destination, reader)
	default:
		return false, fmt.Errorf("unsupported storage provider %q", s.settings.provider)
	}
}

func (s *objectStore) list(prefix string) ([]string, error) {
	if s.settings.provider == "" {
		return nil, nil
	}
	objectPrefix := s.objectKey(prefix)
	keys := make([]string, 0)
	switch s.settings.provider {
	case "s3", "r2":
		paginator := s3.NewListObjectsV2Paginator(s.s3, &s3.ListObjectsV2Input{
			Bucket: aws.String(s.settings.bucket), Prefix: aws.String(objectPrefix),
		})
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(context.Background())
			if err != nil {
				return nil, err
			}
			for _, item := range page.Contents {
				if item.Key != nil {
					keys = append(keys, *item.Key)
				}
			}
		}
	case "gcs":
		it := s.gcs.Bucket(s.settings.bucket).Objects(context.Background(), &storage.Query{Prefix: objectPrefix})
		for {
			attrs, err := it.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				return nil, err
			}
			keys = append(keys, attrs.Name)
		}
	default:
		return nil, fmt.Errorf("unsupported storage provider %q", s.settings.provider)
	}
	return keys, nil
}

func (s *objectStore) objectKey(key string) string {
	parts := []string{s.settings.prefix, s.settings.project, "postgres", key}
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "/")
}

func writeDownloadedFile(destination string, source io.Reader) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return false, err
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
	}
	_, copyErr := io.Copy(file, source)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(destination)
		return false, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(destination)
		return false, closeErr
	}
	return true, nil
}

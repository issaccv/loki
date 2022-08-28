package iharbor

import (
	"context"
	"flag"
	"fmt"
	"github.com/grafana/loki/pkg/storage/chunk/client"
	"io"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/thanos-io/thanos/pkg/objstore"
)

// DirDelim is the delimiter used to model a directory structure in an object store bucket.
// const dirDelim string = "/"

// IHarborConfig stores the configuration for iharbor bucket.
type IHarborConfig struct {
	Endpoint   string `yaml:"endpoint"`
	BucketName string `yaml:"bucket_name"`
	Token      string `yaml:"token"`
	Insecure   bool   `yaml:"insecure"`
	Debug      bool   `yaml:"debug"`
}

// RegisterFlags adds the flags required to config this to the given FlagSet
func (cfg *IHarborConfig) RegisterFlags(f *flag.FlagSet) {
	cfg.RegisterFlagsWithPrefix("", f)
}

// RegisterFlagsWithPrefix adds the flags required to config this to the given FlagSet with a specified prefix
func (cfg *IHarborConfig) RegisterFlagsWithPrefix(prefix string, f *flag.FlagSet) {
	f.StringVar(&cfg.Endpoint, prefix+"iharbor.endpoint", "", "iharbor Endpoint to connect to.")
	f.StringVar(&cfg.BucketName, prefix+"iharbor.bucketname", "", "Comma separated list of bucket names to evenly distribute chunks over. Overrides any buckets specified in s3.url flag")
	f.StringVar(&cfg.Token, prefix+"iharbor.token", "", "iharbor auth token to use.")
	f.BoolVar(&cfg.Insecure, prefix+"iharbor.insecure", false, "Disable https on iharbor connection.")
	f.BoolVar(&cfg.Debug, prefix+"iharbor.debug", false, "Enable debug log")
}

// Validate checks to see if mandatory iharbor config options are set.
func (cfg *IHarborConfig) Validate() error {
	if cfg.BucketName == "" ||
		cfg.Endpoint == "" ||
		cfg.Token == "" {
		return errors.New("insufficient iharbor configuration information," +
			"iharbor endpoint or bucket or token is not present in config file")
	}

	if strings.HasPrefix(cfg.Endpoint, "http") {
		return errors.New("“endpoint”需要是域名，不能包含‘http’")
	}
	return nil
}

// ObjectClient implements the chunk.ObjectClient interface.
type ObjectClient struct {
	name   string
	client *Client
	config IHarborConfig
}

// NewObjectStorage returns a new IharborObjectClient using the provided IharborConfig values.
func NewObjectStorage(tx context.Context, config IHarborConfig) (*ObjectClient, error) {
	if err := config.Validate(); err != nil {
		return nil, errors.Wrap(err, "invalid iharbor Storage config")
	}
	c, err := NewIHarborClient(!config.Insecure, config.Endpoint, config.Token)
	if err != nil {
		return nil, errors.Wrap(err, "create iharbor client failed")
	}

	fmt.Println("success new iharbor object client")
	return &ObjectClient{
		client: c,
		name:   config.BucketName,
		config: config,
	}, nil
}

func (b *ObjectClient) Stop() {}

// PutObject the contents of the reader as an object into the bucket.
func (b *ObjectClient) PutObject(ctx context.Context, objectKey string, object io.ReadSeeker) error {

	err := b.client.PutObject(b.name, objectKey, object)
	if err != nil {
		return errors.Wrapf(err, "failed to PutObject(PutObject) [%s]", objectKey)
	}

	return nil
}

// Upload the contents of the reader as an object into the bucket.
func (b *ObjectClient) Upload(ctx context.Context, objectKey string, object io.ReadSeeker) error {
	size, err := TryToGetSize(object)
	if err != nil {
		return errors.Wrapf(err, "failed to get size to PutObject [%s]", objectKey)
	}

	if size <= 1024*1024*128 { // 128Mb
		err := b.client.PutObject(b.name, objectKey, object)
		if err != nil {
			return errors.Wrapf(err, "failed to PutObject(PutObject) [%s]", objectKey)
		}
	} else {
		err := b.client.MultipartUploadObject(b.name, objectKey, object, 64)
		if err != nil {
			return errors.Wrapf(err, "failed to PutObject(multipart) [%s]", objectKey)
		}
	}

	return nil
}

func (b *ObjectClient) getRange(_ context.Context, objectKey string, off, length int64) (io.ReadCloser, int64, error) {

	if len(objectKey) == 0 {
		return nil, 0, errors.New("given object key should not empty")
	}

	resp, size, err := b.client.GetObject(b.name, objectKey, off, length)
	if err != nil {

		return nil, 0, err
	}

	return resp, size, nil
}

// GetObject returns a reader for the given object name.
func (b *ObjectClient) GetObject(ctx context.Context, objectKey string) (io.ReadCloser, int64, error) {
	resp, size, err := b.getRange(ctx, objectKey, 0, -1)
	return resp, size, err
}

func (b *ObjectClient) GetRange(ctx context.Context, objectKey string, off, length int64) (io.ReadCloser, error) {
	resp, _, err := b.getRange(ctx, objectKey, off, length)
	return resp, err
}

// List calls f for each entry in the given directory (not recursive.). The argument to f is the full
// object name including the prefix of the inspected directory.
func (b *ObjectClient) List(ctx context.Context, prefix string, delimiter string) ([]client.StorageObject, []client.StorageCommonPrefix, error) {
	var storageObjects []client.StorageObject
	var commonPrefixes []client.StorageCommonPrefix

	continuationToken := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, errors.Wrap(err, "context closed while iterating bucket")
		}

		results, err := b.client.ListBucketObjects(b.name, prefix, delimiter, continuationToken, -1)
		if err != nil {
			if b.client.IsNoParentPathErr(err) {
				return storageObjects, commonPrefixes, nil
			}

			return nil, nil, err
		}
		for _, object := range results.Contents {
			if object.IsObject {
				lastModified, err := b.timeStringToTime(object.LastModified)
				if err != nil {
					return nil, nil, errors.Wrap(err, "failed to convert from string type of Object lastModified time")
				}
				storageObjects = append(storageObjects, client.StorageObject{
					Key:        object.Key,
					ModifiedAt: lastModified,
				})
			} else {
				commonPrefix := strings.TrimSuffix(object.Key, delimiter) + delimiter
				commonPrefixes = append(commonPrefixes, client.StorageCommonPrefix(commonPrefix))
			}
		}

		if !results.IsTruncated {
			break
		}
		continuationToken = results.NextContinuationToken
	}

	return storageObjects, commonPrefixes, nil
}

// DeleteObject removes the object with the given name.
func (b *ObjectClient) DeleteObject(ctx context.Context, objectKey string) error {
	if err := b.client.DeleteObject(b.name, objectKey); err != nil {
		return errors.Wrap(err, "delete iharbor object")
	}
	return nil
}

// IsObjectNotFoundErr returns true if error means that object is not found. Relevant to Get operations.
func (b *ObjectClient) IsObjectNotFoundErr(err error) bool {
	return b.client.IsObjNotFoundErr(errors.Cause(err))
}

// Attributes returns information about the specified object.
func (b *ObjectClient) Attributes(ctx context.Context, objectKey string) (objstore.ObjectAttributes, error) {

	meta, err := b.client.GetObjectMeta(b.name, objectKey)
	if err != nil {
		return objstore.ObjectAttributes{}, err
	}

	mtStr := meta.Obj.UpdateTime
	if mtStr == "" {
		mtStr = meta.Obj.UploadTime
	}
	if len(mtStr) == 0 {
		return objstore.ObjectAttributes{}, errors.New("Last modified time no values")
	}
	mod, err := time.Parse(time.RFC3339Nano, mtStr)
	if err != nil {
		return objstore.ObjectAttributes{}, errors.Wrap(err, "parse Last modified")
	}

	return objstore.ObjectAttributes{
		Size:         meta.Obj.Size,
		LastModified: mod,
	}, nil
}

// Exists checks if the given object exists in the bucket.
func (b *ObjectClient) Exists(ctx context.Context, objectKey string) (bool, error) {
	meta, err := b.client.GetObjectMeta(b.name, objectKey)
	if err != nil {
		if b.client.IsObjNotFoundErr(err) {
			return false, nil
		}

		return false, errors.Wrap(err, "cloud not check if object exists")
	}
	if meta.Obj.FileOrDir {
		return true, nil
	}

	return false, nil
}

func (b *ObjectClient) timeStringToTime(s string) (time.Time, error) {
	if s == "" {
		s = "2020-02-14T11:12:44.457024+08:00"
	}
	return time.Parse(time.RFC3339Nano, s)
}

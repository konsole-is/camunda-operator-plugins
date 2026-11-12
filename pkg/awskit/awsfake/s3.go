/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package awsfake

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

const (
	// foreignAccount is the account of the buckets of AddForeignBucket.
	foreignAccount = "444455556666"
	// maxKeys is the most keys that one ListObjectsV2 call returns and that
	// one DeleteObjects call takes.
	maxKeys = 1000
	// maxBucketTags is the most tags of a bucket.
	maxBucketTags = 50
	usEast1       = "us-east-1"
	// nullVersion is the version ID of an object that S3 wrote without
	// versioning on.
	nullVersion = "null"
)

var (
	_ awskit.S3 = (*S3)(nil)
	_ awskit.S3 = s3Region{}

	// bucketName matches the names of general purpose buckets that S3
	// accepts, without its rules on IP addresses and reserved prefixes.
	bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
)

// S3 is an in-memory S3 of every region and of one account. Its methods act
// in Home; In gives the same store seen from another region.
type S3 struct {
	s3Region

	// Account is the account of the buckets that the fake creates. Empty is
	// DefaultAccount.
	Account string
	// Home is the region of the methods of S3. Empty is eu-west-1.
	Home string

	mu sync.Mutex
	// buckets maps a name to its bucket. Names are global, as in S3.
	buckets map[string]*Bucket
	// refused holds bucket/key for each key that DeleteObjects refuses, with
	// the code of the refusal.
	refused map[string]string
	// createErrors holds the error of each create of a bucket name.
	createErrors map[string]error
	// versions counts the delete markers, for their version IDs.
	versions int
	fail     failures
	// late holds the reads of ShowLate, by read operation.
	late map[string]*lateReads
}

// lateReads are the reads of one read operation that show a setting as it
// was before a Put. old maps a bucket name to its copy before the Put.
type lateReads struct {
	reads int
	old   map[string]*oldBucket
}

// oldBucket is a bucket as it was before a Put, and the reads that still
// show it. A negative left shows it until ShowLate releases it.
type oldBucket struct {
	bucket Bucket
	left   int
}

// Bucket is a bucket of the fake. A nil configuration is one that the bucket
// does not have, and its Get call answers with the code of a missing
// configuration.
type Bucket struct {
	Name, Region, Account string
	Tags                  map[string]string
	PublicAccessBlock     *types.PublicAccessBlockConfiguration
	Encryption            *types.ServerSideEncryptionRule
	LifecycleRules        []types.LifecycleRule
	// Objects are the keys of the current objects, in order. ListObjectVersions
	// lists each of them as the version null.
	Objects []string
	// Versioning is the versioning status. Empty is a bucket that never had
	// versioning on.
	Versioning types.BucketVersioningStatus
	// Versions are the noncurrent versions and the delete markers, oldest
	// first. ListObjectsV2 does not list them, and a bucket with one cannot
	// be deleted.
	Versions []ObjectVersion
}

// ObjectVersion is a noncurrent version or a delete marker of a key.
type ObjectVersion struct {
	Key, VersionID string
	DeleteMarker   bool
}

// NewS3 returns an S3 without buckets.
func NewS3() *S3 {
	f := &S3{
		buckets:      map[string]*Bucket{},
		refused:      map[string]string{},
		createErrors: map[string]error{},
		late:         map[string]*lateReads{},
	}
	f.s3Region = s3Region{f: f}
	return f
}

// In returns the S3 of region, with the buckets of f.
func (f *S3) In(region string) awskit.S3 {
	return s3Region{f: f, region: region}
}

// Bucket returns a copy of the bucket name.
func (f *S3) Bucket(name string) (Bucket, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.buckets[name]
	if !ok {
		return Bucket{}, false
	}
	return deepCopy(*b), true
}

// Buckets returns a copy of each bucket, of every account, sorted by name.
func (f *S3) Buckets() []Bucket {
	f.mu.Lock()
	defer f.mu.Unlock()
	buckets := make([]Bucket, 0, len(f.buckets))
	for _, name := range slices.Sorted(maps.Keys(f.buckets)) {
		buckets = append(buckets, deepCopy(*f.buckets[name]))
	}
	return buckets
}

// AddForeignBucket adds the bucket name of another account in region. A call
// of the fake on it is denied, and its name cannot be created.
func (f *S3) AddForeignBucket(name, region string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buckets[name] = &Bucket{Name: name, Region: region, Account: foreignAccount, Tags: map[string]string{}}
}

// PutObjects adds an object for each key to the bucket, as a writer outside
// the plugin does. A key that exists stays one object.
func (f *S3) PutObjects(bucket string, keys ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.buckets[bucket]; ok {
		b.Objects = slices.Compact(slices.Sorted(slices.Values(slices.Concat(b.Objects, keys))))
	}
}

// Edit changes the bucket with edit, as a change outside the plugin does.
func (f *S3) Edit(bucket string, edit func(*Bucket)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.buckets[bucket]; ok {
		edit(b)
	}
}

// RefuseDelete makes each DeleteObjects call keep the object key of bucket
// and report it in Errors with code.
func (f *S3) RefuseDelete(bucket, key, code string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refused[bucket+"/"+key] = code
}

// FailCreate makes each CreateBucket call of the bucket name return err,
// until a call of FailCreate with a nil err.
func (f *S3) FailCreate(name string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.createErrors, name)
		return
	}
	f.createErrors[name] = err
}

// FailNext makes the next call of operation, such as "CreateBucket", in any
// region, return err.
func (f *S3) FailNext(operation string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail.next(operation, err)
}

// ShowLate makes the read operation get show a setting late, as S3 does for a
// while after a write of a bucket configuration. After each Put of the
// setting, the next reads calls of get on the bucket answer the setting as it
// was before the Put. get is GetBucketTagging, GetPublicAccessBlock,
// GetBucketEncryption, or GetBucketLifecycleConfiguration. A negative reads
// shows the old setting until a call with zero reads, which shows each
// setting as it is.
func (f *S3) ShowLate(get string, reads int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if reads == 0 {
		delete(f.late, get)
		return
	}
	f.late[get] = &lateReads{reads: reads, old: map[string]*oldBucket{}}
}

// beforePut keeps a copy of b for the reads of get, when ShowLate made get
// late. A copy that waits from an earlier Put stays. The caller holds the
// lock.
func (f *S3) beforePut(get string, b *Bucket) {
	late, ok := f.late[get]
	if !ok {
		return
	}
	if old, ok := late.old[b.Name]; ok {
		old.left = late.reads
		return
	}
	late.old[b.Name] = &oldBucket{bucket: deepCopy(*b), left: late.reads}
}

// shown returns the bucket that a read of get shows for b: its copy before
// a Put while reads of ShowLate are left, else b. The caller holds the lock.
func (f *S3) shown(get string, b *Bucket) *Bucket {
	late, ok := f.late[get]
	if !ok {
		return b
	}
	old, ok := late.old[b.Name]
	if !ok {
		return b
	}
	if old.left > 0 {
		old.left--
		if old.left == 0 {
			delete(late.old, b.Name)
		}
	}
	return &old.bucket
}

// Calls returns the names of the operations called in every region, in call
// order.
func (f *S3) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.fail.calls)
}

// deepCopy returns a copy of v that shares no pointer, slice, or map with v,
// so that a change through the copy never changes the store.
func deepCopy[T any](v T) T {
	c := reflect.New(reflect.TypeFor[T]()).Elem()
	copyValue(c, reflect.ValueOf(&v).Elem())
	return c.Interface().(T)
}

func copyValue(dst, src reflect.Value) {
	switch src.Kind() {
	case reflect.Pointer:
		if !src.IsNil() {
			dst.Set(reflect.New(src.Elem().Type()))
			copyValue(dst.Elem(), src.Elem())
		}
	case reflect.Struct:
		dst.Set(src)
		for i := range src.NumField() {
			if dst.Field(i).CanSet() {
				copyValue(dst.Field(i), src.Field(i))
			}
		}
	case reflect.Slice:
		if !src.IsNil() {
			dst.Set(reflect.MakeSlice(src.Type(), src.Len(), src.Len()))
			for i := range src.Len() {
				copyValue(dst.Index(i), src.Index(i))
			}
		}
	case reflect.Map:
		if !src.IsNil() {
			dst.Set(reflect.MakeMapWithSize(src.Type(), src.Len()))
			for _, k := range src.MapKeys() {
				v := reflect.New(src.Type().Elem()).Elem()
				copyValue(v, src.MapIndex(k))
				dst.SetMapIndex(k, v)
			}
		}
	case reflect.Interface:
		if !src.IsNil() {
			v := reflect.New(src.Elem().Type()).Elem()
			copyValue(v, src.Elem())
			dst.Set(v)
		}
	default:
		dst.Set(src)
	}
}

// s3Region is the S3 API of one region of a fake. An empty region is the
// Home of the fake.
type s3Region struct {
	f      *S3
	region string
}

// begin locks the fake, records the call of operation, and returns the
// region of r and the error of FailNext, if any. The caller unlocks.
func (r s3Region) begin(operation string) (string, error) {
	r.f.mu.Lock()
	region := cmp.Or(r.region, r.f.Home, defaultHome)
	return region, r.f.fail.call(operation)
}

func (r s3Region) account() string {
	return cmp.Or(r.f.Account, DefaultAccount)
}

// bucket returns the bucket name for a call in region. S3 answers a missing
// bucket with NoSuchBucket, a bucket of another region with
// PermanentRedirect, and a bucket of another account with AccessDenied.
func (r s3Region) bucket(region string, name *string) (*Bucket, error) {
	b, ok := r.f.buckets[aws.ToString(name)]
	switch {
	case !ok:
		return nil, apiError("NoSuchBucket", "The specified bucket does not exist")
	case b.Region != region:
		return nil, apiError(
			"PermanentRedirect",
			"The bucket you are attempting to access must be addressed using the specified endpoint.",
		)
	case b.Account != r.account():
		return nil, apiError("AccessDenied", "Access Denied")
	}
	return b, nil
}

// HeadBucket returns the region of a bucket. A missing bucket is
// *types.NotFound, as the SDK reads the empty answer of S3.
func (r s3Region) HeadBucket(
	_ context.Context,
	in *s3.HeadBucketInput,
	_ ...func(*s3.Options),
) (*s3.HeadBucketOutput, error) {
	region, err := r.begin("HeadBucket")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	b, ok := r.f.buckets[aws.ToString(in.Bucket)]
	switch {
	case !ok:
		return nil, &types.NotFound{}
	case b.Region != region:
		return nil, apiError("MovedPermanently", "Moved Permanently")
	case b.Account != r.account():
		return nil, apiError("Forbidden", "Forbidden")
	}
	return &s3.HeadBucketOutput{BucketRegion: aws.String(b.Region)}, nil
}

// CreateBucket creates a bucket in the region of r, with the tags of its
// configuration, all four flags of the public access block on, and SSE-S3
// default encryption, as AWS gives a new bucket. In us-east-1, a name of a
// bucket that the account owns there is a success that changes nothing.
func (r s3Region) CreateBucket(
	_ context.Context,
	in *s3.CreateBucketInput,
	_ ...func(*s3.Options),
) (*s3.CreateBucketOutput, error) {
	region, err := r.begin("CreateBucket")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	name := aws.ToString(in.Bucket)
	if err := r.f.createErrors[name]; err != nil {
		return nil, err
	}
	if !bucketName.MatchString(name) {
		return nil, apiError("InvalidBucketName", "The specified bucket is not valid.")
	}

	if b, ok := r.f.buckets[name]; ok {
		switch {
		case b.Account != r.account():
			return nil, &types.BucketAlreadyExists{}
		// S3 keeps this answer in us-east-1 for legacy compatibility.
		case region == usEast1 && b.Region == usEast1:
			return &s3.CreateBucketOutput{Location: aws.String("/" + name)}, nil
		}
		return nil, &types.BucketAlreadyOwnedByYou{}
	}

	var config types.CreateBucketConfiguration
	if in.CreateBucketConfiguration != nil {
		config = *in.CreateBucketConfiguration
	}
	if err := checkLocation(region, string(config.LocationConstraint)); err != nil {
		return nil, err
	}

	tags, err := tagSet(config.Tags)
	if err != nil {
		return nil, err
	}

	b := &Bucket{
		Name:    name,
		Region:  region,
		Account: r.account(),
		Tags:    tags,
		PublicAccessBlock: &types.PublicAccessBlockConfiguration{
			BlockPublicAcls:       aws.Bool(true),
			IgnorePublicAcls:      aws.Bool(true),
			BlockPublicPolicy:     aws.Bool(true),
			RestrictPublicBuckets: aws.Bool(true),
		},
		Encryption: &types.ServerSideEncryptionRule{
			ApplyServerSideEncryptionByDefault: &types.ServerSideEncryptionByDefault{
				SSEAlgorithm: types.ServerSideEncryptionAes256,
			},
			BucketKeyEnabled: aws.Bool(false),
		},
	}
	r.f.buckets[name] = b

	return &s3.CreateBucketOutput{Location: aws.String("/" + name)}, nil
}

// checkLocation refuses a location constraint that S3 refuses in region:
// us-east-1 takes none, and every other region takes its own.
func checkLocation(region, constraint string) error {
	switch {
	case region == usEast1 && constraint == region:
		return apiError("InvalidLocationConstraint", "The specified location-constraint is not valid")
	case region == usEast1 && constraint == "":
		return nil
	case constraint != region:
		return apiError(
			"IllegalLocationConstraintException",
			"The %s location constraint is incompatible for the region specific endpoint this request was sent to.",
			cmp.Or(constraint, "unspecified"),
		)
	}
	return nil
}

// DeleteBucket deletes a bucket. A bucket with objects, versions, or delete
// markers is BucketNotEmpty.
func (r s3Region) DeleteBucket(
	_ context.Context,
	in *s3.DeleteBucketInput,
	_ ...func(*s3.Options),
) (*s3.DeleteBucketOutput, error) {
	region, err := r.begin("DeleteBucket")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	b, err := r.bucket(region, in.Bucket)
	if err != nil {
		return nil, err
	}

	if len(b.Objects) > 0 || len(b.Versions) > 0 {
		return nil, apiError("BucketNotEmpty", "The bucket you tried to delete is not empty")
	}
	delete(r.f.buckets, b.Name)
	return &s3.DeleteBucketOutput{}, nil
}

// ListBuckets returns the buckets of the account of every region, sorted by
// name, in pages of MaxBuckets.
func (r s3Region) ListBuckets(
	_ context.Context,
	in *s3.ListBucketsInput,
	_ ...func(*s3.Options),
) (*s3.ListBucketsOutput, error) {
	_, err := r.begin("ListBuckets")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	var all []types.Bucket
	for _, name := range slices.Sorted(maps.Keys(r.f.buckets)) {
		if b := r.f.buckets[name]; b.Account == r.account() {
			all = append(all, types.Bucket{Name: aws.String(b.Name), BucketRegion: aws.String(b.Region)})
		}
	}

	items, next, err := page(all, in.ContinuationToken, int(aws.ToInt32(in.MaxBuckets)))
	if err != nil {
		return nil, err
	}
	return &s3.ListBucketsOutput{Buckets: items, ContinuationToken: next}, nil
}

// GetBucketTagging returns the tags of a bucket, sorted by key. A bucket
// without tags is NoSuchTagSet.
func (r s3Region) GetBucketTagging(
	_ context.Context,
	in *s3.GetBucketTaggingInput,
	_ ...func(*s3.Options),
) (*s3.GetBucketTaggingOutput, error) {
	region, err := r.begin("GetBucketTagging")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	b, err := r.bucket(region, in.Bucket)
	if err != nil {
		return nil, err
	}
	b = r.f.shown("GetBucketTagging", b)

	if len(b.Tags) == 0 {
		return nil, apiError("NoSuchTagSet", "The TagSet does not exist")
	}
	tags := make([]types.Tag, 0, len(b.Tags))
	for _, k := range slices.Sorted(maps.Keys(b.Tags)) {
		tags = append(tags, types.Tag{Key: aws.String(k), Value: aws.String(b.Tags[k])})
	}
	return &s3.GetBucketTaggingOutput{TagSet: tags}, nil
}

// PutBucketTagging replaces every tag of a bucket. More than 50 tags is
// InvalidTag.
func (r s3Region) PutBucketTagging(
	_ context.Context,
	in *s3.PutBucketTaggingInput,
	_ ...func(*s3.Options),
) (*s3.PutBucketTaggingOutput, error) {
	region, err := r.begin("PutBucketTagging")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	b, err := r.bucket(region, in.Bucket)
	if err != nil {
		return nil, err
	}

	if in.Tagging == nil {
		return nil, apiError("MalformedXML", "The XML you provided was not well-formed.")
	}
	tags, err := tagSet(in.Tagging.TagSet)
	if err != nil {
		return nil, err
	}
	r.f.beforePut("GetBucketTagging", b)
	b.Tags = tags
	return &s3.PutBucketTaggingOutput{}, nil
}

// tagSet returns tags as a map. More than 50 tags, or two tags with one key,
// is InvalidTag.
func tagSet(tags []types.Tag) (map[string]string, error) {
	if len(tags) > maxBucketTags {
		return nil, apiError("InvalidTag", "Object tags cannot be greater than %d", maxBucketTags)
	}

	set := make(map[string]string, len(tags))
	for _, t := range tags {
		key := aws.ToString(t.Key)
		if _, ok := set[key]; ok {
			return nil, apiError("InvalidTag", "Cannot provide multiple Tags with the same key")
		}
		set[key] = aws.ToString(t.Value)
	}
	return set, nil
}

// DeleteBucketTagging removes every tag of a bucket.
func (r s3Region) DeleteBucketTagging(
	_ context.Context,
	in *s3.DeleteBucketTaggingInput,
	_ ...func(*s3.Options),
) (*s3.DeleteBucketTaggingOutput, error) {
	region, err := r.begin("DeleteBucketTagging")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	b, err := r.bucket(region, in.Bucket)
	if err != nil {
		return nil, err
	}

	b.Tags = map[string]string{}
	return &s3.DeleteBucketTaggingOutput{}, nil
}

// GetPublicAccessBlock returns the public access block of a bucket. A bucket
// without one is NoSuchPublicAccessBlockConfiguration.
func (r s3Region) GetPublicAccessBlock(
	_ context.Context,
	in *s3.GetPublicAccessBlockInput,
	_ ...func(*s3.Options),
) (*s3.GetPublicAccessBlockOutput, error) {
	region, err := r.begin("GetPublicAccessBlock")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	b, err := r.bucket(region, in.Bucket)
	if err != nil {
		return nil, err
	}
	b = r.f.shown("GetPublicAccessBlock", b)

	if b.PublicAccessBlock == nil {
		return nil, apiError(
			"NoSuchPublicAccessBlockConfiguration",
			"The public access block configuration was not found",
		)
	}
	return &s3.GetPublicAccessBlockOutput{PublicAccessBlockConfiguration: deepCopy(b.PublicAccessBlock)}, nil
}

// PutPublicAccessBlock replaces the public access block of a bucket.
func (r s3Region) PutPublicAccessBlock(
	_ context.Context,
	in *s3.PutPublicAccessBlockInput,
	_ ...func(*s3.Options),
) (*s3.PutPublicAccessBlockOutput, error) {
	region, err := r.begin("PutPublicAccessBlock")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	b, err := r.bucket(region, in.Bucket)
	if err != nil {
		return nil, err
	}

	if in.PublicAccessBlockConfiguration == nil {
		return nil, apiError("MalformedXML", "The XML you provided was not well-formed.")
	}
	r.f.beforePut("GetPublicAccessBlock", b)
	b.PublicAccessBlock = deepCopy(in.PublicAccessBlockConfiguration)
	return &s3.PutPublicAccessBlockOutput{}, nil
}

// GetBucketEncryption returns the default encryption of a bucket. A bucket
// without one is ServerSideEncryptionConfigurationNotFoundError.
func (r s3Region) GetBucketEncryption(
	_ context.Context,
	in *s3.GetBucketEncryptionInput,
	_ ...func(*s3.Options),
) (*s3.GetBucketEncryptionOutput, error) {
	region, err := r.begin("GetBucketEncryption")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	b, err := r.bucket(region, in.Bucket)
	if err != nil {
		return nil, err
	}
	b = r.f.shown("GetBucketEncryption", b)

	if b.Encryption == nil {
		return nil, apiError(
			"ServerSideEncryptionConfigurationNotFoundError",
			"The server side encryption configuration was not found",
		)
	}
	return &s3.GetBucketEncryptionOutput{
		ServerSideEncryptionConfiguration: &types.ServerSideEncryptionConfiguration{
			Rules: []types.ServerSideEncryptionRule{deepCopy(*b.Encryption)},
		},
	}, nil
}

// PutBucketEncryption replaces the default encryption of a bucket with the
// one rule of the call.
func (r s3Region) PutBucketEncryption(
	_ context.Context,
	in *s3.PutBucketEncryptionInput,
	_ ...func(*s3.Options),
) (*s3.PutBucketEncryptionOutput, error) {
	region, err := r.begin("PutBucketEncryption")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	b, err := r.bucket(region, in.Bucket)
	if err != nil {
		return nil, err
	}

	if in.ServerSideEncryptionConfiguration == nil || len(in.ServerSideEncryptionConfiguration.Rules) != 1 {
		return nil, apiError("MalformedXML", "The XML you provided was not well-formed.")
	}
	r.f.beforePut("GetBucketEncryption", b)
	b.Encryption = deepCopy(&in.ServerSideEncryptionConfiguration.Rules[0])
	return &s3.PutBucketEncryptionOutput{}, nil
}

// GetBucketLifecycleConfiguration returns the lifecycle rules of a bucket. A
// bucket without rules is NoSuchLifecycleConfiguration.
func (r s3Region) GetBucketLifecycleConfiguration(
	_ context.Context,
	in *s3.GetBucketLifecycleConfigurationInput,
	_ ...func(*s3.Options),
) (*s3.GetBucketLifecycleConfigurationOutput, error) {
	region, err := r.begin("GetBucketLifecycleConfiguration")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	b, err := r.bucket(region, in.Bucket)
	if err != nil {
		return nil, err
	}
	b = r.f.shown("GetBucketLifecycleConfiguration", b)

	if len(b.LifecycleRules) == 0 {
		return nil, apiError("NoSuchLifecycleConfiguration", "The lifecycle configuration does not exist")
	}
	return &s3.GetBucketLifecycleConfigurationOutput{Rules: deepCopy(b.LifecycleRules)}, nil
}

// PutBucketLifecycleConfiguration replaces every lifecycle rule of a bucket.
func (r s3Region) PutBucketLifecycleConfiguration(
	_ context.Context,
	in *s3.PutBucketLifecycleConfigurationInput,
	_ ...func(*s3.Options),
) (*s3.PutBucketLifecycleConfigurationOutput, error) {
	region, err := r.begin("PutBucketLifecycleConfiguration")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	b, err := r.bucket(region, in.Bucket)
	if err != nil {
		return nil, err
	}

	if in.LifecycleConfiguration == nil || len(in.LifecycleConfiguration.Rules) == 0 {
		return nil, apiError("MalformedXML", "The XML you provided was not well-formed.")
	}
	if err := checkRules(in.LifecycleConfiguration.Rules); err != nil {
		return nil, err
	}
	r.f.beforePut("GetBucketLifecycleConfiguration", b)
	b.LifecycleRules = deepCopy(in.LifecycleConfiguration.Rules)
	return &s3.PutBucketLifecycleConfigurationOutput{}, nil
}

// checkRules refuses a rule without a status, as the SDK does before the
// call, and two rules with one ID, as S3 does.
func checkRules(rules []types.LifecycleRule) error {
	ids := map[string]bool{}
	for _, rule := range rules {
		if rule.Status == "" {
			params := smithy.InvalidParamsError{Context: "LifecycleRule"}
			params.Add(smithy.NewErrParamRequired("Status"))
			return params
		}

		id := aws.ToString(rule.ID)
		if id != "" && ids[id] {
			return apiError("InvalidArgument", "Rule ID must be unique. Found same ID for more than one rule")
		}
		ids[id] = true
	}
	return nil
}

// ListObjectsV2 returns the objects of a bucket in key order, in pages of
// MaxKeys, at most 1,000. A missing bucket is *types.NoSuchBucket.
func (r s3Region) ListObjectsV2(
	_ context.Context,
	in *s3.ListObjectsV2Input,
	_ ...func(*s3.Options),
) (*s3.ListObjectsV2Output, error) {
	region, err := r.begin("ListObjectsV2")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	if _, ok := r.f.buckets[aws.ToString(in.Bucket)]; !ok {
		return nil, &types.NoSuchBucket{Message: aws.String("The specified bucket does not exist")}
	}
	b, err := r.bucket(region, in.Bucket)
	if err != nil {
		return nil, err
	}

	size := maxKeys
	if in.MaxKeys != nil {
		if *in.MaxKeys < 0 {
			return nil, apiError("InvalidArgument", "Argument maxKeys must be an integer between 0 and 2147483647")
		}
		size = min(int(*in.MaxKeys), maxKeys)
	}

	// A token is the last key of the page before, so a delete between two
	// calls moves no key past the next page.
	keys := b.Objects
	if token := aws.ToString(in.ContinuationToken); token != "" {
		start, _ := slices.BinarySearch(keys, token)
		if start < len(keys) && keys[start] == token {
			start++
		}
		keys = keys[start:]
	}

	truncated := len(keys) > size
	keys = keys[:min(size, len(keys))]
	out := &s3.ListObjectsV2Output{
		Name:              in.Bucket,
		KeyCount:          aws.Int32(int32(len(keys))),
		MaxKeys:           aws.Int32(int32(size)),
		IsTruncated:       aws.Bool(truncated),
		ContinuationToken: in.ContinuationToken,
	}
	for _, k := range keys {
		out.Contents = append(out.Contents, types.Object{Key: aws.String(k)})
	}
	if truncated {
		next := aws.ToString(in.ContinuationToken)
		if len(keys) > 0 {
			next = keys[len(keys)-1]
		}
		out.NextContinuationToken = aws.String(next)
	}
	return out, nil
}

// DeleteObjects deletes up to 1,000 objects of a bucket, as S3 does with the
// versioning of the bucket: a key without a version ID gets a delete marker
// when versioning is on or suspended, and a key with a version ID loses that
// version for good. A key of RefuseDelete stays and is reported in Errors; a
// quiet call reports only the errors.
func (r s3Region) DeleteObjects(
	_ context.Context,
	in *s3.DeleteObjectsInput,
	_ ...func(*s3.Options),
) (*s3.DeleteObjectsOutput, error) {
	region, err := r.begin("DeleteObjects")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	b, err := r.bucket(region, in.Bucket)
	if err != nil {
		return nil, err
	}

	if in.Delete == nil || len(in.Delete.Objects) == 0 || len(in.Delete.Objects) > maxKeys {
		return nil, apiError("MalformedXML", "The XML you provided was not well-formed.")
	}

	out := &s3.DeleteObjectsOutput{}
	for _, o := range in.Delete.Objects {
		key := aws.ToString(o.Key)
		if code, ok := r.f.refused[b.Name+"/"+key]; ok {
			out.Errors = append(out.Errors, types.Error{
				Key:     aws.String(key),
				Code:    aws.String(code),
				Message: aws.String("The fake refused the delete"),
			})
			continue
		}
		deleted := r.deleteObject(b, key, o.VersionId)
		if !aws.ToBool(in.Delete.Quiet) {
			out.Deleted = append(out.Deleted, deleted)
		}
	}
	return out, nil
}

// deleteObject deletes key, or its version versionID, from b.
func (r s3Region) deleteObject(b *Bucket, key string, versionID *string) types.DeletedObject {
	deleted := types.DeletedObject{Key: aws.String(key), VersionId: versionID}
	isKey := func(k string) bool { return k == key }

	if versionID != nil {
		if *versionID == nullVersion && slices.Contains(b.Objects, key) {
			b.Objects = slices.DeleteFunc(b.Objects, isKey)
			return deleted
		}
		i := slices.IndexFunc(b.Versions, func(v ObjectVersion) bool {
			return v.Key == key && v.VersionID == *versionID
		})
		if i >= 0 {
			deleted.DeleteMarker = aws.Bool(b.Versions[i].DeleteMarker)
			b.Versions = slices.Delete(b.Versions, i, i+1)
		}
		return deleted
	}

	current := slices.Contains(b.Objects, key)
	b.Objects = slices.DeleteFunc(b.Objects, isKey)

	var marker ObjectVersion
	switch b.Versioning {
	case types.BucketVersioningStatusEnabled:
		if current {
			b.Versions = append(b.Versions, ObjectVersion{Key: key, VersionID: nullVersion})
		}
		r.f.versions++
		marker = ObjectVersion{Key: key, VersionID: fmt.Sprintf("marker-%d", r.f.versions), DeleteMarker: true}
	case types.BucketVersioningStatusSuspended:
		b.Versions = slices.DeleteFunc(b.Versions, func(v ObjectVersion) bool {
			return v.Key == key && v.VersionID == nullVersion
		})
		marker = ObjectVersion{Key: key, VersionID: nullVersion, DeleteMarker: true}
	default:
		return deleted
	}

	b.Versions = append(b.Versions, marker)
	deleted.DeleteMarker = aws.Bool(true)
	deleted.DeleteMarkerVersionId = aws.String(marker.VersionID)
	return deleted
}

// GetBucketVersioning returns the versioning status of a bucket, with no
// status for a bucket that never had versioning on.
func (r s3Region) GetBucketVersioning(
	_ context.Context,
	in *s3.GetBucketVersioningInput,
	_ ...func(*s3.Options),
) (*s3.GetBucketVersioningOutput, error) {
	region, err := r.begin("GetBucketVersioning")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	b, err := r.bucket(region, in.Bucket)
	if err != nil {
		return nil, err
	}
	return &s3.GetBucketVersioningOutput{Status: b.Versioning}, nil
}

// ListObjectVersions returns the versions and the delete markers of a bucket
// in key order, the newest version of each key first, in pages of MaxKeys, at
// most 1,000. Each current object is the version null.
func (r s3Region) ListObjectVersions(
	_ context.Context,
	in *s3.ListObjectVersionsInput,
	_ ...func(*s3.Options),
) (*s3.ListObjectVersionsOutput, error) {
	region, err := r.begin("ListObjectVersions")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	b, err := r.bucket(region, in.Bucket)
	if err != nil {
		return nil, err
	}

	size := maxKeys
	if in.MaxKeys != nil {
		size = min(max(int(*in.MaxKeys), 0), maxKeys)
	}

	all := listedVersions(b)
	start := 0
	if marker := aws.ToString(in.KeyMarker); marker != "" {
		start = len(all)
		for i, v := range all {
			if v.Key == marker && v.VersionID == aws.ToString(in.VersionIdMarker) {
				start = i + 1
				break
			}
			if v.Key > marker {
				start = i
				break
			}
		}
	}
	all = all[start:]

	truncated := len(all) > size
	page := all[:min(size, len(all))]
	out := &s3.ListObjectVersionsOutput{
		Name:            in.Bucket,
		MaxKeys:         aws.Int32(int32(size)),
		IsTruncated:     aws.Bool(truncated),
		KeyMarker:       in.KeyMarker,
		VersionIdMarker: in.VersionIdMarker,
	}
	for _, v := range page {
		if v.DeleteMarker {
			out.DeleteMarkers = append(out.DeleteMarkers, types.DeleteMarkerEntry{
				Key:       aws.String(v.Key),
				VersionId: aws.String(v.VersionID),
				IsLatest:  aws.Bool(v.latest),
			})
			continue
		}
		out.Versions = append(out.Versions, types.ObjectVersion{
			Key:       aws.String(v.Key),
			VersionId: aws.String(v.VersionID),
			IsLatest:  aws.Bool(v.latest),
		})
	}
	if truncated && len(page) > 0 {
		last := page[len(page)-1]
		out.NextKeyMarker, out.NextVersionIdMarker = aws.String(last.Key), aws.String(last.VersionID)
	}
	return out, nil
}

// listedVersion is one item of ListObjectVersions.
type listedVersion struct {
	ObjectVersion
	latest bool
}

// listedVersions returns every version and delete marker of b in key order,
// the newest of each key first.
func listedVersions(b *Bucket) []listedVersion {
	byKey := map[string][]ObjectVersion{}
	for _, k := range b.Objects {
		byKey[k] = nil
	}
	for _, v := range b.Versions {
		byKey[v.Key] = append(byKey[v.Key], v)
	}

	var all []listedVersion
	for _, key := range slices.Sorted(maps.Keys(byKey)) {
		latest := true
		if slices.Contains(b.Objects, key) {
			all = append(all, listedVersion{ObjectVersion{Key: key, VersionID: nullVersion}, true})
			latest = false
		}
		versions := byKey[key]
		for i := len(versions) - 1; i >= 0; i-- {
			all = append(all, listedVersion{versions[i], latest})
			latest = false
		}
	}
	return all
}

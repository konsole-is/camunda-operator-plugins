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

package awss3

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

// The events of the external actions on a bucket, and their action verbs.
const (
	eventReasonBucketCreated       = "BucketCreated"
	eventReasonBucketTagsUpdated   = "BucketTagsUpdated"
	eventReasonPublicAccessBlocked = "PublicAccessBlocked"
	eventReasonEncryptionUpdated   = "EncryptionUpdated"
	eventReasonLifecycleUpdated    = "LifecycleUpdated"
	eventReasonBucketDeleted       = "BucketDeleted"
	eventReasonBucketReleased      = "BucketReleased"
	eventActionCreate              = "Create"
	eventActionUpdate              = "Update"
	eventActionDelete              = "Delete"
	eventActionOrphan              = "Orphan"
)

// abortRuleID is the ID of the one lifecycle rule that the plugin owns.
const abortRuleID = "abort-incomplete-uploads"

// The answers of S3 that the plugin reads by their code.
const (
	codeBucketNotEmpty = "BucketNotEmpty"
	// HeadBucket answers MovedPermanently, and the other calls answer
	// PermanentRedirect, for a bucket of another region.
	codeMovedPermanently  = "MovedPermanently"
	codePermanentRedirect = "PermanentRedirect"
)

// buckets keeps the S3 buckets of requests. s3 gives the client of a region.
type buckets struct {
	s3 func(region string) awskit.S3
	// deleteBudget is the time for which one call of delete deletes objects.
	deleteBudget time.Duration
	// writes keeps the last write of the plugin on each bucket.
	writes *writeTimes
}

// bucketRef names the bucket of one request.
type bucketRef struct {
	Namespace, Claim, Request, UID string
	// Name and Region are the name and the region of the bucket.
	Name, Region string
	// Bound says that status.resources of the request records the bucket.
	Bound bool
}

// want is the desired state of a bucket.
type want struct {
	Tags map[string]string
	// KeyARN is the KMS key of the default encryption. Empty is SSE-S3.
	KeyARN string
	// AbortDays is the age at which S3 aborts an incomplete multipart upload.
	AbortDays int32
}

// prior is what the last served reconcile of the request published.
type prior struct {
	// Served says that it published the bucket.
	Served bool
	// KeyARN is its keyArn output.
	KeyARN string
}

// action is one external action on a bucket, for one event on the request.
type action struct {
	Reason, Verb, Note string
	// Corrected names the drift that the action repaired on a bucket that the
	// request had before. It is empty for a create and for a change of the
	// request.
	Corrected string
}

// bucketInUseError means that the name of the bucket belongs to another
// request, to no request, to another AWS account, or to a bucket in another
// region, or that S3 refuses to show the bucket.
type bucketInUseError struct {
	Bucket, Owner string
	// MaybeOwn says that the bucket can still be the bucket of the request.
	MaybeOwn bool
}

func (e bucketInUseError) Error() string {
	return fmt.Sprintf(
		"The bucket name %s belongs to %s. Set another bucketPrefix to get a bucket of another name",
		e.Bucket,
		e.Owner,
	)
}

// tagsNotShownError means that S3 shows no UID tag on the recorded bucket of
// the request, within the settle time of the last write of the plugin or of
// its first look at the bucket. S3 can show the tags of a new bucket late, so
// the bucket can still be the bucket of the request. The plugin changes,
// empties, and deletes nothing on it. Left is the time until the settle time
// ends.
type tagsNotShownError struct {
	Bucket string
	Left   time.Duration
}

func (e tagsNotShownError) Error() string {
	return fmt.Sprintf("Waiting for S3 to show the tags of the bucket %s", e.Bucket)
}

// settlingError means that a setting of the bucket differs from the desired
// state within the settle time of the last write of the plugin, so ensure did
// not repair it. Left is the time until the settle time ends.
type settlingError struct {
	Bucket string
	Left   time.Duration
}

func (e settlingError) Error() string {
	return fmt.Sprintf("S3 does not show a setting of the bucket %s as the plugin wrote it yet", e.Bucket)
}

// errShownLate means that a step with repairLater found a setting that
// differs from the desired state, and wrote nothing.
var errShownLate = errors.New("S3 can still show the setting as it was before the last write")

// repair says how a step treats a setting that differs from the desired
// state.
type repair int

const (
	// repairChange writes the setting as a part of a create or of a change of
	// the request.
	repairChange repair = iota
	// repairDrift writes the setting as a correction of drift.
	repairDrift
	// repairLater writes nothing, because S3 can still show the setting as it
	// was before the last write of the plugin.
	repairLater
)

// claimable returns bucketInUseError when the bucket of ref exists and does
// not carry the UID of ref, or when headBucket refuses it. It reads only.
func (b buckets) claimable(ctx context.Context, ref bucketRef) error {
	c := b.s3(ref.Region)

	exists, err := headBucket(ctx, c, ref)
	if !exists || err != nil {
		return err
	}

	tags, err := bucketTagsOf(ctx, c, ref.Name)
	if err != nil {
		return err
	}
	if !awskit.OwnedBy(tags, ref.UID) {
		return bucketInUseError{Bucket: ref.Name, Owner: awskit.OwnerOf(tags)}
	}
	return nil
}

// ensure keeps the bucket of ref in the desired state w: it creates the
// bucket with its tags, adds the tags of w that the bucket lacks, blocks
// public access, sets the default encryption, and sets the abort rule. It
// returns what it did as actions, also with an error. It returns
// bucketInUseError, and changes nothing, when the bucket does not carry the
// UID of ref, also after a create that S3 answered with success. It returns
// tagsNotShownError instead while the recorded bucket of ref has no UID tag
// within the settle time. With p.Served, a repair of a bucket that the
// request had is drift. A changed key is not drift: p.KeyARN is the key that
// the plugin wrote last. Within the settle time of the last write of the
// plugin, it writes no public access block, encryption, or abort rule that
// differs, except a changed key, and returns settlingError.
func (b buckets) ensure(ctx context.Context, ref bucketRef, w want, p prior) ([]action, error) {
	c := b.s3(ref.Region)

	created, err := createBucket(ctx, c, ref, w.Tags)
	if err != nil {
		return nil, err
	}

	var actions []action
	if created {
		a := action{
			Reason: eventReasonBucketCreated,
			Verb:   eventActionCreate,
			Note:   fmt.Sprintf("Created the S3 bucket %s in %s", ref.Name, ref.Region),
		}
		if p.Served {
			a.Corrected = "created the bucket " + ref.Name + ", which was gone"
		}
		actions = append(actions, a)
		b.writes.wrote(ref.Name)
	}

	tags, err := bucketTagsOf(ctx, c, ref.Name)
	if err != nil {
		return actions, err
	}
	if !awskit.OwnedBy(tags, ref.UID) {
		if late := b.tagsLate(ref, tags); late != nil {
			return actions, late
		}
		return nil, bucketInUseError{Bucket: ref.Name, Owner: awskit.OwnerOf(tags)}
	}

	left := b.writes.left(ref.Name)
	// The settings of a bucket that the reconcile created are part of the
	// create, also of a create that repairs drift.
	fix := repairDrift
	switch {
	case !p.Served || created:
		fix = repairChange
	case left > 0:
		fix = repairLater
	}
	keyFix := fix
	if p.KeyARN != w.KeyARN {
		keyFix = repairChange
	}

	late := false
	for _, step := range []func() (*action, error){
		func() (*action, error) { return ensureTags(ctx, c, ref.Name, tags, w.Tags) },
		func() (*action, error) { return ensurePublicAccessBlock(ctx, c, ref.Name, fix) },
		func() (*action, error) { return ensureEncryption(ctx, c, ref.Name, w.KeyARN, keyFix) },
		func() (*action, error) { return ensureAbortRule(ctx, c, ref.Name, w.AbortDays, fix) },
	} {
		a, err := step()
		if a != nil {
			actions = append(actions, *a)
			b.writes.wrote(ref.Name)
		}
		if errors.Is(err, errShownLate) {
			late = true
			continue
		}
		if err != nil {
			return actions, err
		}
	}

	if late {
		return actions, settlingError{Bucket: ref.Name, Left: left}
	}
	return actions, nil
}

// createBucket creates the bucket of ref with tags when HeadBucket does not
// find it, and reports whether S3 answered the create with success. It
// returns bucketInUseError for a bucket of another account or of another
// region.
func createBucket(ctx context.Context, c awskit.S3, ref bucketRef, tags map[string]string) (bool, error) {
	exists, err := headBucket(ctx, c, ref)
	if exists || err != nil {
		return false, err
	}

	config := &types.CreateBucketConfiguration{Tags: s3Tags(tags)}
	// us-east-1 refuses its own name as a location constraint.
	if ref.Region != "us-east-1" {
		config.LocationConstraint = types.BucketLocationConstraint(ref.Region)
	}
	_, err = c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(ref.Name), CreateBucketConfiguration: config})

	var taken *types.BucketAlreadyExists
	var owned *types.BucketAlreadyOwnedByYou
	switch {
	case errors.As(err, &taken):
		return false, bucketInUseError{Bucket: ref.Name, Owner: "another AWS account"}
	case errors.As(err, &owned):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("creating the bucket %s: %w", ref.Name, err)
	}
	return true, nil
}

// headBucket reports whether the bucket of ref exists in its region. A
// bucket that S3 refuses to show, and a bucket of the name in another region,
// are bucketInUseError: a create of a name that the account owns resets the
// ACLs of the bucket in us-east-1.
func headBucket(ctx context.Context, c awskit.S3, ref bucketRef) (bool, error) {
	_, err := c.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(ref.Name)})
	switch {
	case err == nil:
		return true, nil
	case awskit.IsNotFound(err):
		return false, nil
	case inOtherRegion(err):
		return false, bucketInUseError{Bucket: ref.Name, Owner: "a bucket of the same name in another region"}
	case awskit.IsAccessDenied(err):
		return false, bucketInUseError{
			Bucket:   ref.Name,
			Owner:    "another AWS account, or a bucket that the controller may not read",
			MaybeOwn: true,
		}
	}
	return false, fmt.Errorf("reading the bucket %s: %w", ref.Name, err)
}

func inOtherRegion(err error) bool {
	code := awskit.ErrorCode(err)
	return code == codeMovedPermanently || code == codePermanentRedirect
}

// s3Tags returns tags sorted by key.
func s3Tags(tags map[string]string) []types.Tag {
	out := make([]types.Tag, 0, len(tags))
	for _, k := range slices.Sorted(maps.Keys(tags)) {
		out = append(out, types.Tag{Key: aws.String(k), Value: aws.String(tags[k])})
	}
	return out
}

// bucketTagsOf returns the tags of the bucket name, none for a bucket without
// a tag set.
func bucketTagsOf(ctx context.Context, c awskit.S3, name string) (map[string]string, error) {
	out, err := c.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: aws.String(name)})
	switch {
	case awskit.IsNotConfigured(err):
		return map[string]string{}, nil
	case inOtherRegion(err):
		return nil, bucketInUseError{Bucket: name, Owner: "a bucket of the same name in another region"}
	case err != nil:
		return nil, fmt.Errorf("reading the tags of the bucket %s: %w", name, err)
	}

	tags := make(map[string]string, len(out.TagSet))
	for _, t := range out.TagSet {
		tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return tags, nil
}

// tagsLate returns a tagsNotShownError when the recorded bucket of ref has no
// UID tag within the settle time, and nil otherwise.
func (b buckets) tagsLate(ref bucketRef, tags map[string]string) error {
	if !ref.Bound || tags[awskit.TagRequestUID] != "" {
		return nil
	}
	if left := b.writes.left(ref.Name); left > 0 {
		return tagsNotShownError{Bucket: ref.Name, Left: left}
	}
	return nil
}

// ensureTags writes the tags of want that current lacks or holds with another
// value, and keeps each tag of current with another key.
func ensureTags(ctx context.Context, c awskit.S3, name string, current, want map[string]string) (*action, error) {
	merged := maps.Clone(current)
	maps.Copy(merged, want)
	if maps.Equal(merged, current) {
		return nil, nil
	}

	_, err := c.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{
		Bucket:  aws.String(name),
		Tagging: &types.Tagging{TagSet: s3Tags(merged)},
	})
	if err != nil {
		return nil, fmt.Errorf("tagging the bucket %s: %w", name, err)
	}
	return &action{
		Reason: eventReasonBucketTagsUpdated,
		Verb:   eventActionUpdate,
		Note:   "Tagged the S3 bucket " + name + " again",
	}, nil
}

// ensurePublicAccessBlock turns on the four flags of the public access block
// of the bucket name when one is off or the block is missing.
func ensurePublicAccessBlock(ctx context.Context, c awskit.S3, name string, fix repair) (*action, error) {
	out, err := c.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: aws.String(name)})
	if err != nil && !awskit.IsNotConfigured(err) {
		return nil, fmt.Errorf("reading the public access block of the bucket %s: %w", name, err)
	}
	if err == nil {
		block := out.PublicAccessBlockConfiguration
		if block != nil && aws.ToBool(block.BlockPublicAcls) && aws.ToBool(block.IgnorePublicAcls) &&
			aws.ToBool(block.BlockPublicPolicy) && aws.ToBool(block.RestrictPublicBuckets) {
			return nil, nil
		}
	}
	if fix == repairLater {
		return nil, errShownLate
	}

	_, err = c.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket: aws.String(name),
		PublicAccessBlockConfiguration: &types.PublicAccessBlockConfiguration{
			BlockPublicAcls:       aws.Bool(true),
			IgnorePublicAcls:      aws.Bool(true),
			BlockPublicPolicy:     aws.Bool(true),
			RestrictPublicBuckets: aws.Bool(true),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("blocking the public access of the bucket %s: %w", name, err)
	}

	a := &action{
		Reason: eventReasonPublicAccessBlocked,
		Verb:   eventActionUpdate,
		Note:   "Blocked the public access of the S3 bucket " + name,
	}
	if fix == repairDrift {
		a.Corrected = "blocked the public access of the bucket " + name + " again"
	}
	return a, nil
}

// ensureEncryption sets the default encryption of the bucket name: SSE-KMS
// with keyARN and an S3 Bucket Key, or SSE-S3 when keyARN is empty.
func ensureEncryption(ctx context.Context, c awskit.S3, name, keyARN string, fix repair) (*action, error) {
	wanted := types.ServerSideEncryptionByDefault{SSEAlgorithm: types.ServerSideEncryptionAes256}
	if keyARN != "" {
		wanted = types.ServerSideEncryptionByDefault{
			SSEAlgorithm:   types.ServerSideEncryptionAwsKms,
			KMSMasterKeyID: aws.String(keyARN),
		}
	}

	out, err := c.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: aws.String(name)})
	if err != nil && !awskit.IsNotConfigured(err) {
		return nil, fmt.Errorf("reading the default encryption of the bucket %s: %w", name, err)
	}
	switch {
	case err == nil && hasEncryption(out.ServerSideEncryptionConfiguration, wanted):
		return nil, nil
	case fix == repairLater:
		return nil, errShownLate
	}

	_, err = c.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{
		Bucket: aws.String(name),
		ServerSideEncryptionConfiguration: &types.ServerSideEncryptionConfiguration{
			Rules: []types.ServerSideEncryptionRule{{
				ApplyServerSideEncryptionByDefault: &wanted,
				BucketKeyEnabled:                   aws.Bool(keyARN != ""),
			}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("setting the default encryption of the bucket %s: %w", name, err)
	}

	a := &action{
		Reason: eventReasonEncryptionUpdated,
		Verb:   eventActionUpdate,
		Note:   "Set the default encryption of the S3 bucket " + name + " to " + encryptionOf(keyARN),
	}
	if fix == repairDrift {
		a.Corrected = "set the default encryption of the bucket " + name + " again"
	}
	return a, nil
}

// hasEncryption reports whether config has exactly the default encryption
// wanted, with an S3 Bucket Key for SSE-KMS.
func hasEncryption(config *types.ServerSideEncryptionConfiguration, wanted types.ServerSideEncryptionByDefault) bool {
	if config == nil || len(config.Rules) != 1 || config.Rules[0].ApplyServerSideEncryptionByDefault == nil {
		return false
	}

	rule := config.Rules[0]
	current := rule.ApplyServerSideEncryptionByDefault
	if current.SSEAlgorithm != wanted.SSEAlgorithm ||
		aws.ToString(current.KMSMasterKeyID) != aws.ToString(wanted.KMSMasterKeyID) {
		return false
	}
	return wanted.SSEAlgorithm != types.ServerSideEncryptionAwsKms || aws.ToBool(rule.BucketKeyEnabled)
}

// encryptionOf names the default encryption of a bucket with the key keyARN.
func encryptionOf(keyARN string) string {
	if keyARN == "" {
		return "SSE-S3"
	}
	return "SSE-KMS with an S3 Bucket Key, key " + keyARN
}

// ensureAbortRule sets the lifecycle rule abortRuleID of the bucket name,
// which aborts an incomplete multipart upload after days, and keeps every
// other rule. It replaces the rule by its ID. Only a missing rule is drift: a
// rule with other days is a change of the request.
func ensureAbortRule(ctx context.Context, c awskit.S3, name string, days int32, fix repair) (*action, error) {
	out, err := c.GetBucketLifecycleConfiguration(
		ctx,
		&s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String(name)},
	)
	if err != nil && !awskit.IsNotConfigured(err) {
		return nil, fmt.Errorf("reading the lifecycle rules of the bucket %s: %w", name, err)
	}

	var rules []types.LifecycleRule
	var minimumSize types.TransitionDefaultMinimumObjectSize
	if err == nil {
		rules, minimumSize = out.Rules, out.TransitionDefaultMinimumObjectSize
	}

	wanted := abortRule(days)
	i := slices.IndexFunc(rules, func(r types.LifecycleRule) bool { return aws.ToString(r.ID) == abortRuleID })
	missing := i < 0
	switch {
	case !missing && sameAbortRule(rules[i], days):
		return nil, nil
	case fix == repairLater:
		return nil, errShownLate
	case missing:
		rules = append(slices.Clone(rules), wanted)
	default:
		rules = slices.Clone(rules)
		rules[i] = wanted
	}

	_, err = c.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket:                             aws.String(name),
		LifecycleConfiguration:             &types.BucketLifecycleConfiguration{Rules: rules},
		TransitionDefaultMinimumObjectSize: minimumSize,
	})
	if err != nil {
		return nil, fmt.Errorf("writing the lifecycle rules of the bucket %s: %w", name, err)
	}

	a := &action{
		Reason: eventReasonLifecycleUpdated,
		Verb:   eventActionUpdate,
		Note: fmt.Sprintf(
			"Set the lifecycle rule %s of the S3 bucket %s to abort an incomplete upload after %d days",
			abortRuleID,
			name,
			days,
		),
	}
	if fix == repairDrift && missing {
		a.Corrected = "added the lifecycle rule " + abortRuleID + " of the bucket " + name + " again"
	}
	return a, nil
}

func abortRule(days int32) types.LifecycleRule {
	return types.LifecycleRule{
		ID:                             aws.String(abortRuleID),
		Status:                         types.ExpirationStatusEnabled,
		Filter:                         &types.LifecycleRuleFilter{Prefix: aws.String("")},
		AbortIncompleteMultipartUpload: &types.AbortIncompleteMultipartUpload{DaysAfterInitiation: aws.Int32(days)},
	}
}

// sameAbortRule reports whether r aborts an incomplete upload of each object
// after days, and does nothing else. The plugin writes its rule with an empty
// filter, so a rule without a filter is not its rule as written.
func sameAbortRule(r types.LifecycleRule, days int32) bool {
	f := r.Filter
	everyObject := f != nil && aws.ToString(f.Prefix) == "" && f.And == nil && f.Tag == nil &&
		f.ObjectSizeGreaterThan == nil && f.ObjectSizeLessThan == nil
	return r.Status == types.ExpirationStatusEnabled && everyObject &&
		r.AbortIncompleteMultipartUpload != nil &&
		aws.ToInt32(r.AbortIncompleteMultipartUpload.DaysAfterInitiation) == days &&
		r.Expiration == nil && len(r.Transitions) == 0 &&
		r.NoncurrentVersionExpiration == nil && len(r.NoncurrentVersionTransitions) == 0
}

// delete deletes the objects, the object versions, and the delete markers of
// the bucket of ref for at most the delete budget, and then the bucket. It
// reports whether objects remain, so that a later call goes on. It deletes
// nothing, and is done, when the bucket is gone, when it does not carry the
// UID of ref, or when it is a bucket of another account or region that the
// request never had.
func (b buckets) delete(ctx context.Context, ref bucketRef) ([]action, bool, error) {
	c := b.s3(ref.Region)

	tags, err := b.ownedTags(ctx, c, ref)
	if tags == nil || err != nil {
		return nil, false, err
	}

	remain, err := awskit.EmptyBucketWithin(ctx, c, ref.Name, b.deleteBudget)
	if remain || err != nil {
		return nil, remain, err
	}

	_, err = c.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(ref.Name)})
	switch {
	case awskit.IsNotFound(err):
		return nil, false, nil
	// A writer put an object after the last pass.
	case awskit.ErrorCode(err) == codeBucketNotEmpty:
		return nil, true, nil
	case err != nil:
		return nil, false, fmt.Errorf("deleting the bucket %s: %w", ref.Name, err)
	}

	b.writes.forget(ref.Name)
	return []action{{
		Reason: eventReasonBucketDeleted,
		Verb:   eventActionDelete,
		Note:   "Deleted the S3 bucket " + ref.Name + " and its objects",
	}}, false, nil
}

// ownedTags returns the tags of the bucket of ref when it carries the UID of
// ref, and nil otherwise. A bucket in another region than ref is not owned. A
// bucket that S3 refuses to show is not owned only when ref is not Bound. It
// returns tagsNotShownError for a bucket that ensure would wait for.
func (b buckets) ownedTags(ctx context.Context, c awskit.S3, ref bucketRef) (map[string]string, error) {
	_, err := c.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(ref.Name)})
	switch {
	case awskit.IsNotFound(err) || inOtherRegion(err):
		return nil, nil
	// The plugin records the bucket before it creates it.
	case !ref.Bound && awskit.IsAccessDenied(err):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("reading the bucket %s: %w", ref.Name, err)
	}

	tags, err := bucketTagsOf(ctx, c, ref.Name)
	if err != nil {
		return nil, err
	}
	if awskit.OwnedBy(tags, ref.UID) {
		return tags, nil
	}
	return nil, b.tagsLate(ref, tags)
}

// release removes the request tags and the UID tag from the bucket of ref.
// The bucket keeps its objects, its settings, and its other tags. It does
// nothing when ownedTags finds that the bucket is not the request's.
func (b buckets) release(ctx context.Context, ref bucketRef) ([]action, error) {
	c := b.s3(ref.Region)

	tags, err := b.ownedTags(ctx, c, ref)
	if tags == nil || err != nil {
		return nil, err
	}
	for _, key := range []string{awskit.TagNamespace, awskit.TagClaim, awskit.TagRequest, awskit.TagRequestUID} {
		delete(tags, key)
	}

	if len(tags) == 0 {
		_, err = c.DeleteBucketTagging(ctx, &s3.DeleteBucketTaggingInput{Bucket: aws.String(ref.Name)})
	} else {
		_, err = c.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{
			Bucket:  aws.String(ref.Name),
			Tagging: &types.Tagging{TagSet: s3Tags(tags)},
		})
	}
	if err != nil {
		return nil, fmt.Errorf("removing the request tags of the bucket %s: %w", ref.Name, err)
	}
	b.writes.forget(ref.Name)

	return []action{{
		Reason: eventReasonBucketReleased,
		Verb:   eventActionOrphan,
		Note:   "Released the S3 bucket " + ref.Name + ". It stays with its objects",
	}}, nil
}

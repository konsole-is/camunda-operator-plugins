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

package awskms

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

// The events of the external actions on a key, and their action verbs.
const (
	eventReasonKeyCreated           = "KeyCreated"
	eventReasonReplicaCreated       = "ReplicaCreated"
	eventReasonKeyPolicyUpdated     = "KeyPolicyUpdated"
	eventReasonKeyRotationEnabled   = "KeyRotationEnabled"
	eventReasonKeyTagsUpdated       = "KeyTagsUpdated"
	eventReasonKeyDeletionScheduled = "KeyDeletionScheduled"
	eventReasonKeyReleased          = "KeyReleased"
	eventActionCreate               = "Create"
	eventActionUpdate               = "Update"
	eventActionDelete               = "Delete"
	eventActionOrphan               = "Orphan"
)

// keyPolicyName is the name of the one key policy of each key.
const keyPolicyName = "default"

// The shortest and the longest window of a scheduled key deletion that KMS
// allows.
const (
	minPendingWindowDays int32 = 7
	maxPendingWindowDays int32 = 30
)

// keys keeps the KMS keys of requests. The primary key of each request is in
// the home region of settings; kms gives the client of a region.
type keys struct {
	settings Settings
	kms      func(region string) awskit.KMS
}

// keyRef names the key of one request.
type keyRef struct {
	Namespace, Claim, Request string
}

// action is one external action on a key, for one event on the request.
type action struct {
	Reason, Verb, Note string
	// Corrected names the drift that the action repaired on a key that
	// existed before the reconcile. It is empty for a create.
	Corrected string
}

// keyStatus is the state of the key of a request.
type keyStatus struct {
	ARN, ID, Alias string
	// Region is the region of the key.
	Region       string
	State        types.KeyState
	MultiRegion  bool
	Rotation     bool
	DeletionDate *time.Time
	// Replicas are the replicas in the regions that the request asks for.
	Replicas []replicaStatus
	// ExtraReplicas are the replicas in other regions, without their state.
	// They stay until the request is deleted.
	ExtraReplicas []replicaStatus
}

type replicaStatus struct {
	Region, ARN string
	State       types.KeyState
}

// aliasInUseError means that the alias of a request names a key of another
// request, or a key that no request holds.
type aliasInUseError struct {
	Alias, KeyARN, Owner string
}

func (e aliasInUseError) Error() string {
	return fmt.Sprintf(
		"The alias %s names the KMS key %s of %s. Delete the alias or move it to another key to free it",
		e.Alias,
		e.KeyARN,
		e.Owner,
	)
}

// invalidReplicaError means that replicaRegions holds the home region.
type invalidReplicaError struct {
	Region string
}

func (e invalidReplicaError) Error() string {
	return fmt.Sprintf("replicaRegions holds %s, the region of the primary key", e.Region)
}

// notMultiRegionError means that replicas are asked for a single-Region key.
type notMultiRegionError struct {
	KeyARN string
}

func (e notMultiRegionError) Error() string {
	return fmt.Sprintf("The KMS key %s is a single-Region key, so it cannot have replicas", e.KeyARN)
}

// desiredKey is what the settings ask of each key and each replica of a
// request. A nil policy keeps the key policy that the key has.
type desiredKey struct {
	tags   []types.Tag
	policy []byte
}

// ensure keeps the key of ref: an enabled key with rotation, the alias, the
// tags, and the key policy of the settings, multi-Region with a replica in
// each of replicaRegions when that list is not empty. It returns what it did
// as actions, also with an error. It returns invalidReplicaError before any
// call, aliasInUseError when the alias names a key of another request, and
// notMultiRegionError when replicas are asked for a single-Region key. A key
// that is not Enabled is reported as it is and not changed. A replica outside
// replicaRegions is kept.
func (k keys) ensure(ctx context.Context, ref keyRef, replicaRegions []string) (keyStatus, []action, error) {
	if slices.Contains(replicaRegions, k.settings.Region) {
		return keyStatus{}, nil, invalidReplicaError{Region: k.settings.Region}
	}

	policy, _, err := keyPolicy(k.settings)
	if err != nil {
		return keyStatus{}, nil, err
	}
	want := desiredKey{tags: keyTags(k.settings, ref.Namespace, ref.Claim, ref.Request), policy: policy}

	home := k.kms(k.settings.Region)
	alias := aliasName(k.settings.AliasPrefix, ref.Namespace, ref.Claim)

	var actions []action
	meta, err := describeKey(ctx, home, alias)
	created := awskit.IsNotFound(err)
	switch {
	case created:
		meta, err = k.create(ctx, home, ref, alias, want, len(replicaRegions) > 0)
		if err != nil {
			return keyStatus{}, nil, err
		}
		actions = append(actions, action{
			Reason: eventReasonKeyCreated,
			Verb:   eventActionCreate,
			Note:   fmt.Sprintf("Created the KMS key %s with the alias %s", aws.ToString(meta.Arn), alias),
		})
	case err != nil:
		return keyStatus{}, nil, fmt.Errorf("reading the KMS key of alias %q: %w", alias, err)
	}

	status := keyStatus{
		ARN:          aws.ToString(meta.Arn),
		ID:           aws.ToString(meta.KeyId),
		Alias:        alias,
		Region:       k.settings.Region,
		State:        meta.KeyState,
		MultiRegion:  aws.ToBool(meta.MultiRegion),
		DeletionDate: meta.DeletionDate,
	}

	if !created {
		current, err := listTags(ctx, home, status.ARN)
		if err != nil {
			return keyStatus{}, nil, err
		}
		if !owns(current, ref) {
			return keyStatus{}, nil, aliasInUseError{Alias: alias, KeyARN: status.ARN, Owner: owner(current)}
		}
		if status.State != types.KeyStateEnabled {
			return status, nil, nil
		}

		repaired, err := repair(ctx, home, status.ARN, "key", current, want)
		actions = append(actions, repaired...)
		if err != nil {
			return status, actions, err
		}
	}

	rotation, err := ensureRotation(ctx, home, status.ARN, created)
	if rotation != nil {
		actions = append(actions, *rotation)
	}
	if err != nil {
		return status, actions, err
	}
	status.Rotation = true

	if len(replicaRegions) > 0 && !status.MultiRegion {
		return status, actions, notMultiRegionError{KeyARN: status.ARN}
	}
	if !status.MultiRegion {
		return status, actions, nil
	}

	replicated, err := k.ensureReplicas(ctx, &status, meta, ref, replicaRegions, want)
	return status, append(actions, replicated...), err
}

// describeKey returns the metadata of the key that keyID names: a key ID, a
// key ARN, or an alias. The error of a key that does not exist satisfies
// awskit.IsNotFound.
func describeKey(ctx context.Context, c awskit.KMS, keyID string) (*types.KeyMetadata, error) {
	out, err := c.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(keyID)})
	if err != nil {
		return nil, err
	}
	return out.KeyMetadata, nil
}

// create creates the key of ref and its alias. When the alias fails, it
// schedules the deletion of the new key, so that a retry leaves no key
// without an alias behind.
func (k keys) create(
	ctx context.Context,
	home awskit.KMS,
	ref keyRef,
	alias string,
	want desiredKey,
	multiRegion bool,
) (*types.KeyMetadata, error) {
	in := &kms.CreateKeyInput{
		Description: aws.String(description(ref)),
		KeySpec:     types.KeySpecSymmetricDefault,
		KeyUsage:    types.KeyUsageTypeEncryptDecrypt,
		Origin:      types.OriginTypeAwsKms,
		MultiRegion: aws.Bool(multiRegion),
		Tags:        want.tags,
	}
	if want.policy != nil {
		in.Policy = aws.String(string(want.policy))
	}

	out, err := home.CreateKey(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("creating the KMS key of alias %q: %w", alias, err)
	}
	key := out.KeyMetadata

	_, err = home.CreateAlias(ctx, &kms.CreateAliasInput{AliasName: aws.String(alias), TargetKeyId: key.KeyId})
	if err == nil {
		return key, nil
	}

	// AWS can commit the alias and lose the answer, and the retry of the SDK
	// then fails with AlreadyExistsException.
	if target, describeErr := describeKey(ctx, home, alias); describeErr == nil &&
		aws.ToString(target.KeyId) == aws.ToString(key.KeyId) {
		return key, nil
	}

	err = fmt.Errorf("creating the alias %q of the KMS key %s: %w", alias, aws.ToString(key.Arn), err)
	_, scheduleErr := home.ScheduleKeyDeletion(ctx, &kms.ScheduleKeyDeletionInput{
		KeyId:               key.KeyId,
		PendingWindowInDays: aws.Int32(minPendingWindowDays),
	})
	if scheduleErr != nil {
		scheduleErr = fmt.Errorf(
			"scheduling the deletion of the KMS key %s without an alias: %w",
			aws.ToString(key.Arn),
			scheduleErr,
		)
	}
	return nil, errors.Join(err, scheduleErr)
}

func description(ref keyRef) string {
	return "Camunda cluster " + ref.Namespace + "/" + ref.Claim
}

// listTags returns the tags of the key keyID.
func listTags(ctx context.Context, c awskit.KMS, keyID string) (map[string]string, error) {
	tags := map[string]string{}
	in := &kms.ListResourceTagsInput{KeyId: aws.String(keyID)}
	for {
		out, err := c.ListResourceTags(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("reading the tags of the KMS key %s: %w", keyID, err)
		}
		for _, t := range out.Tags {
			tags[aws.ToString(t.TagKey)] = aws.ToString(t.TagValue)
		}
		if !out.Truncated {
			return tags, nil
		}
		in.Marker = out.NextMarker
	}
}

// owns reports whether a key with tags belongs to the request of ref.
func owns(tags map[string]string, ref keyRef) bool {
	return tags[awskit.TagNamespace] == ref.Namespace && tags[awskit.TagRequest] == ref.Request
}

// owner names the request of a key with tags, for a message.
func owner(tags map[string]string) string {
	request, ok := tags[awskit.TagRequest]
	if !ok {
		return "no request"
	}
	return "the request " + tags[awskit.TagNamespace] + "/" + request
}

// repair tags the key keyARN again when a tag of want is missing from
// current or differs, and with a policy in want writes the key policy again
// when it differs. what names the key in the events and in Corrected. It
// never removes a tag.
func repair(
	ctx context.Context,
	c awskit.KMS,
	keyARN, what string,
	current map[string]string,
	want desiredKey,
) ([]action, error) {
	var actions []action

	if slices.ContainsFunc(want.tags, func(t types.Tag) bool {
		value, ok := current[aws.ToString(t.TagKey)]
		return !ok || value != aws.ToString(t.TagValue)
	}) {
		// Every tag goes in each request, because the sandbox refuses a tag
		// request without its run tag.
		if _, err := c.TagResource(ctx, &kms.TagResourceInput{KeyId: aws.String(keyARN), Tags: want.tags}); err != nil {
			return actions, fmt.Errorf("tagging the KMS %s %s: %w", what, keyARN, err)
		}
		actions = append(actions, action{
			Reason:    eventReasonKeyTagsUpdated,
			Verb:      eventActionUpdate,
			Note:      fmt.Sprintf("Tagged the KMS %s %s again", what, keyARN),
			Corrected: what + " tags",
		})
	}

	if want.policy == nil {
		return actions, nil
	}

	out, err := c.GetKeyPolicy(
		ctx,
		&kms.GetKeyPolicyInput{KeyId: aws.String(keyARN), PolicyName: aws.String(keyPolicyName)},
	)
	if err != nil {
		return actions, fmt.Errorf("reading the key policy of the KMS %s %s: %w", what, keyARN, err)
	}
	if awskit.SamePolicy([]byte(aws.ToString(out.Policy)), want.policy) {
		return actions, nil
	}

	_, err = c.PutKeyPolicy(ctx, &kms.PutKeyPolicyInput{
		KeyId:      aws.String(keyARN),
		PolicyName: aws.String(keyPolicyName),
		Policy:     aws.String(string(want.policy)),
	})
	if err != nil {
		return actions, fmt.Errorf("writing the key policy of the KMS %s %s: %w", what, keyARN, err)
	}
	return append(actions, action{
		Reason:    eventReasonKeyPolicyUpdated,
		Verb:      eventActionUpdate,
		Note:      fmt.Sprintf("Updated the key policy of the KMS %s %s", what, keyARN),
		Corrected: what + " policy",
	}), nil
}

// ensureRotation turns on the automatic rotation of the key keyARN when it is
// off, and returns the action when it did. A new key has rotation off, so
// its action is not a correction.
func ensureRotation(ctx context.Context, c awskit.KMS, keyARN string, created bool) (*action, error) {
	if !created {
		out, err := c.GetKeyRotationStatus(ctx, &kms.GetKeyRotationStatusInput{KeyId: aws.String(keyARN)})
		if err != nil {
			return nil, fmt.Errorf("reading the rotation of the KMS key %s: %w", keyARN, err)
		}
		if out.KeyRotationEnabled {
			return nil, nil
		}
	}

	if _, err := c.EnableKeyRotation(ctx, &kms.EnableKeyRotationInput{KeyId: aws.String(keyARN)}); err != nil {
		return nil, fmt.Errorf("turning on the rotation of the KMS key %s: %w", keyARN, err)
	}

	a := action{
		Reason: eventReasonKeyRotationEnabled,
		Verb:   eventActionUpdate,
		Note:   fmt.Sprintf("Turned on the automatic rotation of the KMS key %s", keyARN),
	}
	if !created {
		a.Corrected = "key rotation"
	}
	return &a, nil
}

// ensureReplicas creates the replica of the primary key meta in each of
// regions that it lacks, reads the state of each replica, and repairs the
// tags and the policy of each enabled replica that existed before. It sets
// the replicas of status: those in regions, and as extras the others.
func (k keys) ensureReplicas(
	ctx context.Context,
	status *keyStatus,
	meta *types.KeyMetadata,
	ref keyRef,
	regions []string,
	want desiredKey,
) ([]action, error) {
	existing := map[string]string{}
	if config := meta.MultiRegionConfiguration; config != nil {
		for _, r := range config.ReplicaKeys {
			existing[aws.ToString(r.Region)] = aws.ToString(r.Arn)
		}
	}

	var actions []action
	for _, region := range regions {
		replica, ok := existing[region]
		created := !ok
		if created {
			in := &kms.ReplicateKeyInput{
				KeyId:         aws.String(status.ARN),
				ReplicaRegion: aws.String(region),
				Description:   aws.String(description(ref)),
				Tags:          want.tags,
			}
			if want.policy != nil {
				in.Policy = aws.String(string(want.policy))
			}

			out, err := k.kms(k.settings.Region).ReplicateKey(ctx, in)
			if err != nil {
				return actions, fmt.Errorf("replicating the KMS key %s to %s: %w", status.ARN, region, err)
			}
			replica = aws.ToString(out.ReplicaKeyMetadata.Arn)
			actions = append(actions, action{
				Reason: eventReasonReplicaCreated,
				Verb:   eventActionCreate,
				Note:   fmt.Sprintf("Created the replica %s of the KMS key %s", replica, status.ARN),
			})
		}

		r, repaired, err := k.checkReplica(ctx, region, replica, !created, want)
		actions = append(actions, repaired...)
		if err != nil {
			return actions, err
		}
		status.Replicas = append(status.Replicas, r)
	}

	for _, region := range slices.Sorted(maps.Keys(existing)) {
		if slices.Contains(regions, region) {
			continue
		}

		r, repaired, err := k.checkReplica(ctx, region, existing[region], true, want)
		actions = append(actions, repaired...)
		if err != nil {
			return actions, err
		}
		status.ExtraReplicas = append(status.ExtraReplicas, r)
	}

	return actions, nil
}

// checkReplica reads the state of the replica replicaARN in region, and with
// repairDrift repairs its tags and its key policy when it is enabled.
func (k keys) checkReplica(
	ctx context.Context,
	region, replicaARN string,
	repairDrift bool,
	want desiredKey,
) (replicaStatus, []action, error) {
	c := k.kms(region)
	meta, err := describeKey(ctx, c, replicaARN)
	if err != nil {
		return replicaStatus{}, nil, fmt.Errorf("reading the replica %s: %w", replicaARN, err)
	}
	status := replicaStatus{Region: region, ARN: replicaARN, State: meta.KeyState}

	if !repairDrift || meta.KeyState != types.KeyStateEnabled {
		return status, nil, nil
	}

	current, err := listTags(ctx, c, replicaARN)
	if err != nil {
		return status, nil, err
	}
	actions, err := repair(ctx, c, replicaARN, "replica", current, want)
	return status, actions, err
}

// scheduleDeletion schedules the deletion of the key of ref and of each of
// its replicas after windowDays, and deletes its alias, so that a new claim
// of the same name gets a new key. It does nothing when the alias is gone or
// names a key of another request. A key that is pending deletion already is
// done.
func (k keys) scheduleDeletion(ctx context.Context, ref keyRef, windowDays int32) ([]action, error) {
	home := k.kms(k.settings.Region)
	alias := aliasName(k.settings.AliasPrefix, ref.Namespace, ref.Claim)

	meta, ok, err := k.ownedKey(ctx, home, alias, ref)
	if !ok || err != nil {
		return nil, err
	}

	var replicas []types.MultiRegionKey
	if meta.MultiRegionConfiguration != nil {
		replicas = meta.MultiRegionConfiguration.ReplicaKeys
	}

	// KMS deletes a primary key only after its replicas.
	for _, r := range replicas {
		if err := schedule(ctx, k.kms(aws.ToString(r.Region)), aws.ToString(r.Arn), windowDays); err != nil {
			return nil, err
		}
	}
	if err := schedule(ctx, home, aws.ToString(meta.Arn), windowDays); err != nil {
		return nil, err
	}

	_, err = home.DeleteAlias(ctx, &kms.DeleteAliasInput{AliasName: aws.String(alias)})
	if err != nil && !awskit.IsNotFound(err) {
		return nil, fmt.Errorf("deleting the alias %q: %w", alias, err)
	}

	return []action{{
		Reason: eventReasonKeyDeletionScheduled,
		Verb:   eventActionDelete,
		Note: fmt.Sprintf(
			"Scheduled the deletion of the KMS key %s and %d replicas in %d days, and deleted the alias %s",
			aws.ToString(meta.Arn),
			len(replicas),
			windowDays,
			alias,
		),
	}}, nil
}

// ownedKey returns the key that alias names, and true when it belongs to the
// request of ref.
func (k keys) ownedKey(
	ctx context.Context,
	home awskit.KMS,
	alias string,
	ref keyRef,
) (*types.KeyMetadata, bool, error) {
	meta, err := describeKey(ctx, home, alias)
	if awskit.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading the KMS key of alias %q: %w", alias, err)
	}

	tags, err := listTags(ctx, home, aws.ToString(meta.Arn))
	if err != nil {
		return nil, false, err
	}
	return meta, owns(tags, ref), nil
}

// schedule schedules the deletion of the key keyARN. A key that is gone or
// pending deletion already is done. A key in another state that KMS cannot
// schedule is an error.
func schedule(ctx context.Context, c awskit.KMS, keyARN string, windowDays int32) error {
	_, err := c.ScheduleKeyDeletion(ctx, &kms.ScheduleKeyDeletionInput{
		KeyId:               aws.String(keyARN),
		PendingWindowInDays: aws.Int32(windowDays),
	})
	if err == nil || awskit.IsNotFound(err) {
		return nil
	}

	if awskit.ErrorCode(err) == "KMSInvalidStateException" {
		meta, describeErr := describeKey(ctx, c, keyARN)
		if describeErr == nil && pendingDeletion(meta.KeyState) {
			return nil
		}
	}
	return fmt.Errorf("scheduling the deletion of the KMS key %s: %w", keyARN, err)
}

func pendingDeletion(state types.KeyState) bool {
	return state == types.KeyStatePendingDeletion || state == types.KeyStatePendingReplicaDeletion
}

// release removes the request tags from the key of ref and from each of its
// replicas. The key stays enabled and keeps its alias and its extra tags, so
// a new request of the same claim gets aliasInUseError and never takes the
// key over. It does nothing when the alias is gone or names a key of another
// request.
func (k keys) release(ctx context.Context, ref keyRef) ([]action, error) {
	home := k.kms(k.settings.Region)
	alias := aliasName(k.settings.AliasPrefix, ref.Namespace, ref.Claim)

	meta, ok, err := k.ownedKey(ctx, home, alias, ref)
	if !ok || err != nil {
		return nil, err
	}

	var untag []replicaStatus
	if meta.MultiRegionConfiguration != nil {
		for _, r := range meta.MultiRegionConfiguration.ReplicaKeys {
			untag = append(untag, replicaStatus{Region: aws.ToString(r.Region), ARN: aws.ToString(r.Arn)})
		}
	}
	// The primary goes last: its request tag is how a retry finds the key as
	// the request's.
	untag = append(untag, replicaStatus{Region: k.settings.Region, ARN: aws.ToString(meta.Arn)})

	for _, u := range untag {
		_, err := k.kms(u.Region).UntagResource(ctx, &kms.UntagResourceInput{
			KeyId:   aws.String(u.ARN),
			TagKeys: []string{awskit.TagNamespace, awskit.TagClaim, awskit.TagRequest},
		})
		if err != nil && !awskit.IsNotFound(err) {
			return nil, fmt.Errorf("removing the request tags of the KMS key %s: %w", u.ARN, err)
		}
	}

	return []action{{
		Reason: eventReasonKeyReleased,
		Verb:   eventActionOrphan,
		Note: fmt.Sprintf(
			"Released the KMS key %s. It stays enabled with the alias %s",
			aws.ToString(meta.Arn),
			alias,
		),
	}}, nil
}

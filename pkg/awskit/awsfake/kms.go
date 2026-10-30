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
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

// defaultHome is the region of a KMS that sets no Home.
const defaultHome = "eu-west-1"

var (
	_ awskit.KMS = (*KMS)(nil)
	_ awskit.KMS = kmsRegion{}
)

// KMS is an in-memory KMS of every region. Its methods act in Home; In gives
// the same store seen from another region.
type KMS struct {
	kmsRegion

	// Account is the account of the ARNs. Empty is DefaultAccount.
	Account string
	// Home is the region of the methods of KMS. Empty is eu-west-1.
	Home string

	mu sync.Mutex
	// keys maps region/ID to a key.
	keys map[string]*Key
	// aliases maps region/alias name to a key ID.
	aliases map[string]string
	seq     int
	fail    failures
}

// Key is a key of the fake. A replica has the ID of its primary.
type Key struct {
	ARN, ID, Region, Description string
	State                        types.KeyState
	MultiRegion, Rotation        bool
	// DefaultPolicy says that Policy is the default key policy, because no
	// call gave one.
	DefaultPolicy     bool
	Policy            string
	Tags              map[string]string
	PendingWindowDays int32
	DeletionDate      *time.Time
	// Primary is the region of the primary key of a multi-Region key, and
	// empty for a single-Region key.
	Primary string
}

// NewKMS returns a KMS without keys.
func NewKMS() *KMS {
	f := &KMS{keys: map[string]*Key{}, aliases: map[string]string{}}
	f.kmsRegion = kmsRegion{f: f}
	return f
}

// In returns the KMS of region, with the keys and the aliases of f.
func (f *KMS) In(region string) awskit.KMS {
	return kmsRegion{f: f, region: region}
}

// Key returns a copy of the key keyID in region.
func (f *KMS) Key(region, keyID string) (Key, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.keys[region+"/"+keyID]
	if !ok {
		return Key{}, false
	}
	return k.clone(), true
}

// Keys returns a copy of each key, sorted by region, then by ID.
func (f *KMS) Keys() []Key {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := make([]Key, 0, len(f.keys))
	for _, k := range f.keys {
		keys = append(keys, k.clone())
	}
	slices.SortFunc(keys, func(a, b Key) int {
		return cmp.Or(cmp.Compare(a.Region, b.Region), cmp.Compare(a.ID, b.ID))
	})
	return keys
}

// AliasTarget returns the ID of the key that the alias names in region.
func (f *KMS) AliasTarget(region, alias string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.aliases[region+"/"+alias]
	return id, ok
}

// SetState sets the state of the key keyID in region, as a change outside
// the plugin does. PendingDeletion also sets a deletion date in 30 days.
func (f *KMS) SetState(region, keyID string, state types.KeyState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if k, ok := f.keys[region+"/"+keyID]; ok {
		k.State = state
		if state == types.KeyStatePendingDeletion {
			k.schedule(30)
		}
	}
}

// SetRotation turns the automatic rotation of the key keyID in region on or
// off, as a change outside the plugin does.
func (f *KMS) SetRotation(region, keyID string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if k, ok := f.keys[region+"/"+keyID]; ok {
		k.Rotation = on
	}
}

// FailNext makes the next call of operation, such as "CreateAlias", in any
// region, return err.
func (f *KMS) FailNext(operation string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail.next(operation, err)
}

// Calls returns the names of the operations called in every region, in call
// order.
func (f *KMS) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.fail.calls)
}

func (k *Key) clone() Key {
	c := *k
	c.Tags = maps.Clone(k.Tags)
	return c
}

func (k *Key) schedule(days int32) {
	date := time.Now().Add(time.Duration(days) * 24 * time.Hour)
	k.PendingWindowDays = days
	k.DeletionDate = &date
}

// kmsRegion is the KMS API of one region of a fake. An empty region is the
// Home of the fake.
type kmsRegion struct {
	f      *KMS
	region string
}

// begin locks the fake, records the call of operation, and returns the
// region of r and the error of FailNext, if any. The caller unlocks.
func (r kmsRegion) begin(operation string) (string, error) {
	r.f.mu.Lock()
	region := cmp.Or(r.region, r.f.Home, defaultHome)
	return region, r.f.fail.call(operation)
}

func (r kmsRegion) account() string {
	return cmp.Or(r.f.Account, DefaultAccount)
}

// key returns the key that keyID names in region: a key ID, a key ARN, or an
// alias name.
func (r kmsRegion) key(region string, keyID *string) (*Key, error) {
	id := aws.ToString(keyID)
	if strings.HasPrefix(id, "alias/") {
		target, ok := r.f.aliases[region+"/"+id]
		if !ok {
			return nil, apiError("NotFoundException", "Alias %s is not found.", id)
		}
		id = target
	} else if parsed, err := arn.Parse(id); err == nil {
		if parsed.Region != region {
			return nil, apiError("NotFoundException", "Key '%s' does not exist", id)
		}
		id = strings.TrimPrefix(parsed.Resource, "key/")
	}

	k, ok := r.f.keys[region+"/"+id]
	if !ok {
		return nil, apiError("NotFoundException", "Key '%s' does not exist", aws.ToString(keyID))
	}
	return k, nil
}

func (r kmsRegion) defaultPolicy() string {
	return `{"Version":"2012-10-17","Id":"key-default-1","Statement":[{"Sid":"Enable IAM User Permissions",` +
		`"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::` + r.account() + `:root"},"Action":"kms:*",` +
		`"Resource":"*"}]}`
}

// newKey adds a key to the store with tags, and policy or the default one.
func (r kmsRegion) newKey(region, id, description, policy string, tags []types.Tag) *Key {
	k := &Key{
		ARN:           "arn:aws:kms:" + region + ":" + r.account() + ":key/" + id,
		ID:            id,
		Region:        region,
		Description:   description,
		State:         types.KeyStateEnabled,
		DefaultPolicy: policy == "",
		Policy:        cmp.Or(policy, r.defaultPolicy()),
		Tags:          map[string]string{},
	}
	for _, t := range tags {
		k.Tags[aws.ToString(t.TagKey)] = aws.ToString(t.TagValue)
	}
	r.f.keys[region+"/"+id] = k
	return k
}

// CreateKey creates an enabled key. A multi-Region key gets an ID that starts
// with mrk-. Without a policy, the key gets the default key policy.
func (r kmsRegion) CreateKey(
	_ context.Context,
	in *kms.CreateKeyInput,
	_ ...func(*kms.Options),
) (*kms.CreateKeyOutput, error) {
	region, err := r.begin("CreateKey")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	if in.Policy != nil && !json.Valid([]byte(*in.Policy)) {
		return nil, apiError("MalformedPolicyDocumentException", "The key policy is not JSON.")
	}

	r.f.seq++
	id := fmt.Sprintf("%08x-0000-4000-8000-%012x", r.f.seq, r.f.seq)
	multiRegion := aws.ToBool(in.MultiRegion)
	if multiRegion {
		id = fmt.Sprintf("mrk-%032x", r.f.seq)
	}

	k := r.newKey(region, id, aws.ToString(in.Description), aws.ToString(in.Policy), in.Tags)
	if multiRegion {
		k.MultiRegion, k.Primary = true, region
	}

	return &kms.CreateKeyOutput{KeyMetadata: r.metadata(k)}, nil
}

// metadata returns the metadata of k as DescribeKey returns it.
func (r kmsRegion) metadata(k *Key) *types.KeyMetadata {
	m := &types.KeyMetadata{
		Arn:          aws.String(k.ARN),
		KeyId:        aws.String(k.ID),
		Description:  aws.String(k.Description),
		KeyState:     k.State,
		Enabled:      k.State == types.KeyStateEnabled,
		MultiRegion:  aws.Bool(k.MultiRegion),
		DeletionDate: k.DeletionDate,
		KeySpec:      types.KeySpecSymmetricDefault,
		KeyUsage:     types.KeyUsageTypeEncryptDecrypt,
		Origin:       types.OriginTypeAwsKms,
	}
	if k.PendingWindowDays != 0 {
		m.PendingDeletionWindowInDays = aws.Int32(k.PendingWindowDays)
	}
	if !k.MultiRegion {
		return m
	}

	primary := r.f.keys[k.Primary+"/"+k.ID]
	config := &types.MultiRegionConfiguration{
		MultiRegionKeyType: types.MultiRegionKeyTypeReplica,
		PrimaryKey:         &types.MultiRegionKey{Arn: aws.String(primary.ARN), Region: aws.String(primary.Region)},
	}
	if k == primary {
		config.MultiRegionKeyType = types.MultiRegionKeyTypePrimary
	}
	for _, key := range slices.Sorted(maps.Keys(r.f.keys)) {
		other := r.f.keys[key]
		if other.ID == k.ID && other != primary {
			config.ReplicaKeys = append(
				config.ReplicaKeys,
				types.MultiRegionKey{Arn: aws.String(other.ARN), Region: aws.String(other.Region)},
			)
		}
	}
	m.MultiRegionConfiguration = config
	return m
}

// DescribeKey returns the metadata of a key named by its ID, its ARN, or an
// alias. A replica in state Creating is Enabled from the next call on.
func (r kmsRegion) DescribeKey(
	_ context.Context,
	in *kms.DescribeKeyInput,
	_ ...func(*kms.Options),
) (*kms.DescribeKeyOutput, error) {
	region, err := r.begin("DescribeKey")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	k, err := r.key(region, in.KeyId)
	if err != nil {
		return nil, err
	}

	out := &kms.DescribeKeyOutput{KeyMetadata: r.metadata(k)}
	if k.State == types.KeyStateCreating {
		k.State = types.KeyStateEnabled
	}
	return out, nil
}

// CreateAlias creates an alias of a key. It refuses an alias that exists.
func (r kmsRegion) CreateAlias(
	_ context.Context,
	in *kms.CreateAliasInput,
	_ ...func(*kms.Options),
) (*kms.CreateAliasOutput, error) {
	region, err := r.begin("CreateAlias")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	name := aws.ToString(in.AliasName)
	if !strings.HasPrefix(name, "alias/") {
		return nil, apiError("ValidationException", "Alias must start with the prefix \"alias/\".")
	}
	if _, ok := r.f.aliases[region+"/"+name]; ok {
		return nil, apiError("AlreadyExistsException", "An alias with the name %s already exists", name)
	}

	k, err := r.key(region, in.TargetKeyId)
	if err != nil {
		return nil, err
	}
	r.f.aliases[region+"/"+name] = k.ID
	return &kms.CreateAliasOutput{}, nil
}

// DeleteAlias deletes an alias. The key stays.
func (r kmsRegion) DeleteAlias(
	_ context.Context,
	in *kms.DeleteAliasInput,
	_ ...func(*kms.Options),
) (*kms.DeleteAliasOutput, error) {
	region, err := r.begin("DeleteAlias")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	name := region + "/" + aws.ToString(in.AliasName)
	if _, ok := r.f.aliases[name]; !ok {
		return nil, apiError("NotFoundException", "Alias %s is not found.", aws.ToString(in.AliasName))
	}
	delete(r.f.aliases, name)
	return &kms.DeleteAliasOutput{}, nil
}

// ListAliases returns the aliases of the region in one page, or only those of
// a key when the input names one.
func (r kmsRegion) ListAliases(
	_ context.Context,
	in *kms.ListAliasesInput,
	_ ...func(*kms.Options),
) (*kms.ListAliasesOutput, error) {
	region, err := r.begin("ListAliases")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	var target string
	if in.KeyId != nil {
		k, err := r.key(region, in.KeyId)
		if err != nil {
			return nil, err
		}
		target = k.ID
	}

	out := &kms.ListAliasesOutput{}
	for _, key := range slices.Sorted(maps.Keys(r.f.aliases)) {
		name, ok := strings.CutPrefix(key, region+"/")
		id := r.f.aliases[key]
		if ok && (target == "" || id == target) {
			out.Aliases = append(
				out.Aliases,
				types.AliasListEntry{AliasName: aws.String(name), TargetKeyId: aws.String(id)},
			)
		}
	}
	return out, nil
}

// ListResourceTags returns the tags of a key, sorted by key.
func (r kmsRegion) ListResourceTags(
	_ context.Context,
	in *kms.ListResourceTagsInput,
	_ ...func(*kms.Options),
) (*kms.ListResourceTagsOutput, error) {
	region, err := r.begin("ListResourceTags")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	k, err := r.key(region, in.KeyId)
	if err != nil {
		return nil, err
	}

	out := &kms.ListResourceTagsOutput{}
	for _, key := range slices.Sorted(maps.Keys(k.Tags)) {
		out.Tags = append(out.Tags, types.Tag{TagKey: aws.String(key), TagValue: aws.String(k.Tags[key])})
	}
	return out, nil
}

// TagResource adds tags to a key, and replaces the value of a key it has.
func (r kmsRegion) TagResource(
	_ context.Context,
	in *kms.TagResourceInput,
	_ ...func(*kms.Options),
) (*kms.TagResourceOutput, error) {
	region, err := r.begin("TagResource")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	k, err := r.key(region, in.KeyId)
	if err != nil {
		return nil, err
	}
	for _, t := range in.Tags {
		k.Tags[aws.ToString(t.TagKey)] = aws.ToString(t.TagValue)
	}
	return &kms.TagResourceOutput{}, nil
}

// UntagResource removes tags from a key.
func (r kmsRegion) UntagResource(
	_ context.Context,
	in *kms.UntagResourceInput,
	_ ...func(*kms.Options),
) (*kms.UntagResourceOutput, error) {
	region, err := r.begin("UntagResource")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	k, err := r.key(region, in.KeyId)
	if err != nil {
		return nil, err
	}
	for _, key := range in.TagKeys {
		delete(k.Tags, key)
	}
	return &kms.UntagResourceOutput{}, nil
}

// GetKeyPolicy returns the policy "default" of a key.
func (r kmsRegion) GetKeyPolicy(
	_ context.Context,
	in *kms.GetKeyPolicyInput,
	_ ...func(*kms.Options),
) (*kms.GetKeyPolicyOutput, error) {
	region, err := r.begin("GetKeyPolicy")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	k, err := r.key(region, in.KeyId)
	if err != nil {
		return nil, err
	}
	if name := aws.ToString(in.PolicyName); name != "" && name != "default" {
		return nil, apiError("NotFoundException", "Policy %s does not exist", name)
	}
	return &kms.GetKeyPolicyOutput{Policy: aws.String(k.Policy), PolicyName: aws.String("default")}, nil
}

// PutKeyPolicy replaces the policy of a key. It refuses a policy that is not
// JSON.
func (r kmsRegion) PutKeyPolicy(
	_ context.Context,
	in *kms.PutKeyPolicyInput,
	_ ...func(*kms.Options),
) (*kms.PutKeyPolicyOutput, error) {
	region, err := r.begin("PutKeyPolicy")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	k, err := r.key(region, in.KeyId)
	if err != nil {
		return nil, err
	}
	if !json.Valid([]byte(aws.ToString(in.Policy))) {
		return nil, apiError("MalformedPolicyDocumentException", "The key policy is not JSON.")
	}
	k.Policy, k.DefaultPolicy = aws.ToString(in.Policy), false
	return &kms.PutKeyPolicyOutput{}, nil
}

// GetKeyRotationStatus returns whether automatic rotation is on. A replica
// has the rotation of its primary key, as KMS shares it.
func (r kmsRegion) GetKeyRotationStatus(
	_ context.Context,
	in *kms.GetKeyRotationStatusInput,
	_ ...func(*kms.Options),
) (*kms.GetKeyRotationStatusOutput, error) {
	region, err := r.begin("GetKeyRotationStatus")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	k, err := r.key(region, in.KeyId)
	if err != nil {
		return nil, err
	}
	rotation := k.Rotation
	if primary, ok := r.f.keys[k.Primary+"/"+k.ID]; ok {
		rotation = primary.Rotation
	}
	return &kms.GetKeyRotationStatusOutput{KeyId: aws.String(k.ARN), KeyRotationEnabled: rotation}, nil
}

// EnableKeyRotation turns automatic rotation on.
func (r kmsRegion) EnableKeyRotation(
	_ context.Context,
	in *kms.EnableKeyRotationInput,
	_ ...func(*kms.Options),
) (*kms.EnableKeyRotationOutput, error) {
	region, err := r.begin("EnableKeyRotation")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	k, err := r.key(region, in.KeyId)
	if err != nil {
		return nil, err
	}
	k.Rotation = true
	return &kms.EnableKeyRotationOutput{}, nil
}

// ReplicateKey creates a replica of a multi-Region primary key in another
// region, with the ID of the primary. The replica starts in state Creating.
// It refuses a single-Region key and a second replica in one region.
func (r kmsRegion) ReplicateKey(
	_ context.Context,
	in *kms.ReplicateKeyInput,
	_ ...func(*kms.Options),
) (*kms.ReplicateKeyOutput, error) {
	region, err := r.begin("ReplicateKey")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	k, err := r.key(region, in.KeyId)
	if err != nil {
		return nil, err
	}
	if !k.MultiRegion || k.Primary != region {
		return nil, apiError("UnsupportedOperationException", "%s is not a multi-Region primary key", k.ARN)
	}

	to := aws.ToString(in.ReplicaRegion)
	if _, ok := r.f.keys[to+"/"+k.ID]; ok {
		return nil, apiError("AlreadyExistsException", "Key %s already has a replica in %s", k.ARN, to)
	}

	replica := r.newKey(to, k.ID, aws.ToString(in.Description), aws.ToString(in.Policy), in.Tags)
	replica.State, replica.MultiRegion, replica.Primary = types.KeyStateCreating, true, region

	return &kms.ReplicateKeyOutput{ReplicaKeyMetadata: r.metadata(replica)}, nil
}

// ScheduleKeyDeletion schedules the deletion of a key after a window of 7 to
// 30 days, 30 when the input sets none. A primary key with a replica goes to
// PendingReplicaDeletion with no deletion date, because KMS starts its window
// only after its replicas are deleted, which the fake never does. It refuses
// a key that is pending deletion already, or in state Creating.
func (r kmsRegion) ScheduleKeyDeletion(
	_ context.Context,
	in *kms.ScheduleKeyDeletionInput,
	_ ...func(*kms.Options),
) (*kms.ScheduleKeyDeletionOutput, error) {
	region, err := r.begin("ScheduleKeyDeletion")
	defer r.f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	k, err := r.key(region, in.KeyId)
	if err != nil {
		return nil, err
	}

	days := aws.ToInt32(in.PendingWindowInDays)
	if in.PendingWindowInDays == nil {
		days = 30
	}
	if days < 7 || days > 30 {
		return nil, apiError("ValidationException", "PendingWindowInDays must be between 7 and 30.")
	}
	switch k.State {
	case types.KeyStatePendingDeletion, types.KeyStatePendingReplicaDeletion, types.KeyStateCreating:
		return nil, apiError("KMSInvalidStateException", "%s is %s.", k.ARN, k.State)
	}

	if k.MultiRegion && k.Primary == region && slices.ContainsFunc(
		slices.Collect(maps.Values(r.f.keys)),
		func(o *Key) bool { return o.ID == k.ID && o != k },
	) {
		k.State, k.PendingWindowDays = types.KeyStatePendingReplicaDeletion, days
	} else {
		k.State = types.KeyStatePendingDeletion
		k.schedule(days)
	}

	return &kms.ScheduleKeyDeletionOutput{
		KeyId:               aws.String(k.ARN),
		KeyState:            k.State,
		DeletionDate:        k.DeletionDate,
		PendingWindowInDays: aws.Int32(days),
	}, nil
}

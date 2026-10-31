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
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
)

const (
	home    = "eu-west-1"
	replica = "eu-central-1"
)

var ref = keyRef{Namespace: "team-a", Claim: "orders", Request: "orders-encryption-key", UID: "uid-a"}

func newKeys(fake *awsfake.KMS, edit ...func(*Settings)) keys {
	s := Settings{
		Region:      home,
		Partition:   "aws",
		Account:     awsfake.DefaultAccount,
		AliasPrefix: "camunda",
		ExtraTags:   awskit.ExtraTags{"cpo-ci-run": "cpo-ci-test"},
	}
	for _, e := range edit {
		e(&s)
	}
	return keys{settings: s, kms: fake.In}
}

func withAdmins(admins ...string) func(*Settings) {
	return func(s *Settings) { s.KeyAdministratorARNs = admins }
}

func reasons(actions []action) []string {
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		out = append(out, a.Reason)
	}
	return out
}

func count(calls []string, operation string) int {
	n := 0
	for _, c := range calls {
		if c == operation {
			n++
		}
	}
	return n
}

func mustKey(t *testing.T, fake *awsfake.KMS, region, id string) awsfake.Key {
	t.Helper()
	k, ok := fake.Key(region, id)
	require.True(t, ok, "the key %s in %s exists", id, region)
	return k
}

const wantAlias = "alias/camunda/team-a/orders"

func TestEnsureCreatesARotatingKeyWithItsAlias(t *testing.T) {
	fake := awsfake.NewKMS()

	status, actions, err := newKeys(fake).ensure(context.Background(), ref, nil)

	require.NoError(t, err)
	key := mustKey(t, fake, home, status.ID)
	assert.Equal(t, types.KeyStateEnabled, key.State)
	assert.True(t, key.Rotation)
	assert.True(t, key.DefaultPolicy)
	assert.False(t, key.MultiRegion)
	assert.Equal(t, map[string]string{
		awskit.TagNamespace: "team-a",
		awskit.TagClaim:     "orders",
		awskit.TagRequest:   "orders-encryption-key",
		tagRequestUID:       "uid-a",
		"cpo-ci-run":        "cpo-ci-test",
	}, key.Tags)
	target, _ := fake.AliasTarget(home, wantAlias)
	assert.Equal(t, status.ID, target)
	assert.Equal(t, key.ARN, status.ARN)
	assert.True(t, status.Rotation)
	assert.Equal(t, []string{eventReasonKeyCreated, eventReasonKeyRotationEnabled}, reasons(actions))
	for _, a := range actions {
		assert.Empty(t, a.Corrected, "a new key is no drift")
	}
	assert.NotContains(t, fake.Calls(), "GetKeyPolicy")
	assert.NotContains(t, fake.Calls(), "PutKeyPolicy")
}

func TestEnsureIsIdempotent(t *testing.T) {
	fake := awsfake.NewKMS()
	k := newKeys(fake)
	first, _, err := k.ensure(context.Background(), ref, nil)
	require.NoError(t, err)

	second, actions, err := k.ensure(context.Background(), ref, nil)

	require.NoError(t, err)
	assert.Equal(t, first.ARN, second.ARN)
	assert.Empty(t, actions)
	assert.Equal(t, 1, count(fake.Calls(), "CreateKey"))
}

func TestEnsureNeverReadsThePolicyWithoutPolicyValues(t *testing.T) {
	fake := awsfake.NewKMS()
	k := newKeys(fake)

	for range 2 {
		_, _, err := k.ensure(context.Background(), ref, []string{replica})
		require.NoError(t, err)
	}

	assert.NotContains(t, fake.Calls(), "GetKeyPolicy")
	assert.NotContains(t, fake.Calls(), "PutKeyPolicy")
}

func TestEnsureCreatesAKeyWithTheCustomPolicy(t *testing.T) {
	fake := awsfake.NewKMS()
	k := newKeys(fake, withAdmins(admin))

	status, _, err := k.ensure(context.Background(), ref, nil)

	require.NoError(t, err)
	key := mustKey(t, fake, home, status.ID)
	want, _, err := keyPolicy(k.settings)
	require.NoError(t, err)
	assert.False(t, key.DefaultPolicy)
	assert.JSONEq(t, string(want), key.Policy)
}

func TestEnsureUpdatesAChangedKeyPolicy(t *testing.T) {
	fake := awsfake.NewKMS()
	status, _, err := newKeys(fake, withAdmins(admin)).ensure(context.Background(), ref, nil)
	require.NoError(t, err)
	k := newKeys(fake, withAdmins(admin, "arn:aws:iam::111122223333:role/second"))

	_, actions, err := k.ensure(context.Background(), ref, nil)

	require.NoError(t, err)
	assert.Contains(t, mustKey(t, fake, home, status.ID).Policy, "role/second")
	require.Len(t, actions, 1)
	assert.Equal(t, eventReasonKeyPolicyUpdated, actions[0].Reason)
	assert.Equal(t, "key policy", actions[0].Corrected)
}

func TestEnsureTagsAKeyAgain(t *testing.T) {
	fake := awsfake.NewKMS()
	k := newKeys(fake)
	status, _, err := k.ensure(context.Background(), ref, nil)
	require.NoError(t, err)
	_, err = fake.UntagResource(context.Background(), &kms.UntagResourceInput{
		KeyId:   aws.String(status.ID),
		TagKeys: []string{"cpo-ci-run"},
	})
	require.NoError(t, err)

	_, actions, err := k.ensure(context.Background(), ref, nil)

	require.NoError(t, err)
	assert.Equal(t, "cpo-ci-test", mustKey(t, fake, home, status.ID).Tags["cpo-ci-run"])
	require.Len(t, actions, 1)
	assert.Equal(t, eventReasonKeyTagsUpdated, actions[0].Reason)
	assert.Equal(t, "key tags", actions[0].Corrected)
}

func TestEnsureTurnsTheRotationOnAgain(t *testing.T) {
	fake := awsfake.NewKMS()
	k := newKeys(fake)
	status, _, err := k.ensure(context.Background(), ref, nil)
	require.NoError(t, err)
	fake.SetRotation(home, status.ID, false)

	_, actions, err := k.ensure(context.Background(), ref, nil)

	require.NoError(t, err)
	assert.True(t, mustKey(t, fake, home, status.ID).Rotation)
	require.Len(t, actions, 1)
	assert.Equal(t, "key rotation", actions[0].Corrected)
}

func TestEnsureCreatesTheReplicasOfAMultiRegionKey(t *testing.T) {
	fake := awsfake.NewKMS()
	k := newKeys(fake)

	first, actions, err := k.ensure(context.Background(), ref, []string{replica})
	require.NoError(t, err)
	second, _, err := k.ensure(context.Background(), ref, []string{replica})
	require.NoError(t, err)

	assert.Regexp(t, `^mrk-`, first.ID)
	assert.True(t, first.MultiRegion)
	r := mustKey(t, fake, replica, first.ID)
	assert.Equal(t, mustKey(t, fake, home, first.ID).Tags, r.Tags)
	assert.True(t, r.DefaultPolicy)
	require.Len(t, first.Replicas, 1)
	assert.Equal(t, replicaStatus{Region: replica, ARN: r.ARN, State: types.KeyStateCreating}, first.Replicas[0])
	assert.Equal(t, types.KeyStateEnabled, second.Replicas[0].State)
	assert.Equal(
		t,
		[]string{eventReasonKeyCreated, eventReasonKeyRotationEnabled, eventReasonReplicaCreated},
		reasons(actions),
	)
}

func TestEnsureRefusesTheHomeRegionAsReplica(t *testing.T) {
	fake := awsfake.NewKMS()

	_, _, err := newKeys(fake).ensure(context.Background(), ref, []string{replica, home})

	assert.Equal(t, invalidReplicaError{Region: home}, err)
	assert.Empty(t, fake.Calls())
}

func TestEnsureKeepsAReplicaOutsideTheParameter(t *testing.T) {
	fake := awsfake.NewKMS()
	k := newKeys(fake)
	created, _, err := k.ensure(context.Background(), ref, []string{replica, "us-east-1"})
	require.NoError(t, err)

	status, _, err := k.ensure(context.Background(), ref, []string{replica})

	require.NoError(t, err)
	extra := mustKey(t, fake, "us-east-1", created.ID)
	assert.Equal(
		t,
		[]replicaStatus{{Region: "us-east-1", ARN: extra.ARN, State: types.KeyStateEnabled}},
		status.ExtraReplicas,
	)
	assert.Len(t, status.Replicas, 1)
}

func TestEnsureRepairsAReplicaOutsideTheParameter(t *testing.T) {
	fake := awsfake.NewKMS()
	k := newKeys(fake)
	created, _, err := k.ensure(context.Background(), ref, []string{replica})
	require.NoError(t, err)
	_, err = fake.In(replica).UntagResource(context.Background(), &kms.UntagResourceInput{
		KeyId:   aws.String(created.ID),
		TagKeys: []string{"cpo-ci-run"},
	})
	require.NoError(t, err)

	status, actions, err := k.ensure(context.Background(), ref, nil)

	require.NoError(t, err)
	assert.Empty(t, status.Replicas)
	require.Len(t, status.ExtraReplicas, 1)
	assert.Equal(t, replica, status.ExtraReplicas[0].Region)
	assert.Equal(t, "cpo-ci-test", mustKey(t, fake, replica, created.ID).Tags["cpo-ci-run"])
	require.Len(t, actions, 1)
	assert.Equal(t, "replica tags", actions[0].Corrected)
}

func TestEnsureReportsASingleRegionKeyForReplicas(t *testing.T) {
	fake := awsfake.NewKMS()
	k := newKeys(fake)
	created, _, err := k.ensure(context.Background(), ref, nil)
	require.NoError(t, err)

	status, _, err := k.ensure(context.Background(), ref, []string{replica})

	assert.Equal(t, notMultiRegionError{KeyARN: created.ARN}, err)
	assert.Equal(t, created.ARN, status.ARN)
	assert.NotContains(t, fake.Calls(), "ReplicateKey")
}

func TestEnsureRefusesTheAliasOfAnotherRequest(t *testing.T) {
	fake := awsfake.NewKMS()
	k := newKeys(fake)
	other := ref
	other.Request, other.UID = "orders-other", "uid-other"
	created, _, err := k.ensure(context.Background(), other, nil)
	require.NoError(t, err)
	before := mustKey(t, fake, home, created.ID)

	_, _, err = k.ensure(context.Background(), ref, nil)

	var inUse aliasInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Equal(t, aliasInUseError{
		Alias:  wantAlias,
		KeyARN: created.ARN,
		Owner:  "the request team-a/orders-other with the UID uid-other",
	}, inUse)
	assert.Equal(t, before, mustKey(t, fake, home, created.ID))
}

// otherCluster creates the key of ref with another UID, as a request of the
// same namespace and name in another Kubernetes cluster does.
func otherCluster(t *testing.T, k keys) keyStatus {
	t.Helper()
	other := ref
	other.UID = "uid-of-another-cluster"
	created, _, err := k.ensure(context.Background(), other, []string{replica})
	require.NoError(t, err)
	return created
}

func TestEnsureRefusesAKeyOfTheSameRequestNameInAnotherCluster(t *testing.T) {
	fake := awsfake.NewKMS()
	k := newKeys(fake)
	created := otherCluster(t, k)
	before := mustKey(t, fake, home, created.ID)

	_, actions, err := k.ensure(context.Background(), ref, []string{replica})

	var inUse aliasInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Equal(t, "the request team-a/orders-encryption-key with the UID uid-of-another-cluster", inUse.Owner)
	assert.Empty(t, actions)
	assert.Equal(t, before, mustKey(t, fake, home, created.ID))
	assert.Len(t, fake.Keys(), 2, "the primary and the replica of the other cluster, and no key of ref")
}

func TestScheduleDeletionAndReleaseLeaveAKeyOfTheSameRequestNameInAnotherCluster(t *testing.T) {
	fake := awsfake.NewKMS()
	k := newKeys(fake)
	created := otherCluster(t, k)
	before := fake.Keys()

	deleted, err := k.scheduleDeletion(context.Background(), ref, 7)
	require.NoError(t, err)
	released, err := k.release(context.Background(), ref)
	require.NoError(t, err)

	assert.Empty(t, deleted)
	assert.Empty(t, released)
	assert.Equal(t, before, fake.Keys(), "no state and no tag of the other key changed")
	target, _ := fake.AliasTarget(home, wantAlias)
	assert.Equal(t, created.ID, target)
	assert.NotContains(t, fake.Calls(), "ScheduleKeyDeletion")
	assert.NotContains(t, fake.Calls(), "UntagResource")
}

func TestEnsureRefusesTheAliasOfAnOrphanedKey(t *testing.T) {
	fake := awsfake.NewKMS()
	k := newKeys(fake)
	_, _, err := k.ensure(context.Background(), ref, nil)
	require.NoError(t, err)
	_, err = k.release(context.Background(), ref)
	require.NoError(t, err)

	_, _, err = k.ensure(context.Background(), ref, nil)

	var inUse aliasInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Equal(t, "no request", inUse.Owner)
	assert.Len(t, fake.Keys(), 1)
}

func TestEnsureSchedulesTheNewKeyWhenTheAliasFails(t *testing.T) {
	fake := awsfake.NewKMS()
	fake.FailNext("CreateAlias", errors.New("boom"))

	_, _, err := newKeys(fake).ensure(context.Background(), ref, nil)

	assert.ErrorContains(t, err, "boom")
	keys := fake.Keys()
	require.Len(t, keys, 1)
	assert.Equal(t, types.KeyStatePendingDeletion, keys[0].State)
	assert.Equal(t, int32(7), keys[0].PendingWindowDays)
}

// lostAnswer creates the alias and then fails, as a call does whose answer
// AWS lost.
type lostAnswer struct {
	awskit.KMS
}

func (l lostAnswer) CreateAlias(
	ctx context.Context,
	in *kms.CreateAliasInput,
	opts ...func(*kms.Options),
) (*kms.CreateAliasOutput, error) {
	if _, err := l.KMS.CreateAlias(ctx, in, opts...); err != nil {
		return nil, err
	}
	return nil, errors.New("lost answer")
}

func TestEnsureKeepsTheKeyWhenTheAnswerOfTheAliasIsLost(t *testing.T) {
	fake := awsfake.NewKMS()
	k := newKeys(fake)
	k.kms = func(region string) awskit.KMS { return lostAnswer{fake.In(region)} }

	status, _, err := k.ensure(context.Background(), ref, nil)

	require.NoError(t, err)
	assert.Equal(t, types.KeyStateEnabled, mustKey(t, fake, home, status.ID).State)
	assert.NotContains(t, fake.Calls(), "ScheduleKeyDeletion")
}

func TestEnsureReportsAKeyPendingDeletion(t *testing.T) {
	fake := awsfake.NewKMS()
	k := newKeys(fake)
	created, _, err := k.ensure(context.Background(), ref, nil)
	require.NoError(t, err)
	fake.SetState(home, created.ID, types.KeyStatePendingDeletion)

	status, actions, err := k.ensure(context.Background(), ref, nil)

	require.NoError(t, err)
	assert.Equal(t, types.KeyStatePendingDeletion, status.State)
	assert.NotNil(t, status.DeletionDate)
	assert.Empty(t, actions)
	assert.Equal(t, 1, count(fake.Calls(), "CreateKey"))
}

func TestScheduleDeletionUsesTheWindow(t *testing.T) {
	fake := awsfake.NewKMS()
	k := newKeys(fake)
	created, _, err := k.ensure(context.Background(), ref, []string{replica})
	require.NoError(t, err)

	actions, err := k.scheduleDeletion(context.Background(), ref, 7)

	require.NoError(t, err)
	primary := mustKey(t, fake, home, created.ID)
	r := mustKey(t, fake, replica, created.ID)
	assert.Equal(t, types.KeyStatePendingDeletion, r.State)
	assert.Equal(t, int32(7), r.PendingWindowDays)
	assert.Equal(t, types.KeyStatePendingReplicaDeletion, primary.State)
	assert.Equal(t, int32(7), primary.PendingWindowDays)
	_, ok := fake.AliasTarget(home, wantAlias)
	assert.False(t, ok, "the alias is gone")
	assert.Equal(t, []string{eventReasonKeyDeletionScheduled}, reasons(actions))
}

func TestScheduleDeletionIsDoneForAKeyAlreadyPending(t *testing.T) {
	fake := awsfake.NewKMS()
	k := newKeys(fake)
	created, _, err := k.ensure(context.Background(), ref, nil)
	require.NoError(t, err)
	fake.SetState(home, created.ID, types.KeyStatePendingDeletion)

	_, err = k.scheduleDeletion(context.Background(), ref, 7)

	require.NoError(t, err)
	_, ok := fake.AliasTarget(home, wantAlias)
	assert.False(t, ok, "the alias is gone")
}

func TestScheduleDeletionFailsWhileAReplicaIsCreating(t *testing.T) {
	fake := awsfake.NewKMS()
	k := newKeys(fake)
	created, _, err := k.ensure(context.Background(), ref, []string{replica})
	require.NoError(t, err)
	fake.SetState(replica, created.ID, types.KeyStateCreating)

	_, err = k.scheduleDeletion(context.Background(), ref, 7)

	require.Error(t, err)
	_, ok := fake.AliasTarget(home, wantAlias)
	assert.True(t, ok, "the alias stays until the replica is scheduled")
	assert.Equal(t, types.KeyStateEnabled, mustKey(t, fake, home, created.ID).State)

	_, err = k.scheduleDeletion(context.Background(), ref, 7)

	require.NoError(t, err, "the retry schedules the replica once it is enabled")
	assert.Equal(t, types.KeyStatePendingDeletion, mustKey(t, fake, replica, created.ID).State)
}

func TestScheduleDeletionIsDoneWhenTheAliasIsGone(t *testing.T) {
	fake := awsfake.NewKMS()

	actions, err := newKeys(fake).scheduleDeletion(context.Background(), ref, 7)

	require.NoError(t, err)
	assert.Empty(t, actions)
}

func TestScheduleDeletionLeavesTheKeyOfAnotherRequest(t *testing.T) {
	fake := awsfake.NewKMS()
	k := newKeys(fake)
	other := ref
	other.Request, other.UID = "orders-other", "uid-other"
	created, _, err := k.ensure(context.Background(), other, nil)
	require.NoError(t, err)

	actions, err := k.scheduleDeletion(context.Background(), ref, 7)

	require.NoError(t, err)
	assert.Empty(t, actions)
	assert.Equal(t, types.KeyStateEnabled, mustKey(t, fake, home, created.ID).State)
	_, ok := fake.AliasTarget(home, wantAlias)
	assert.True(t, ok, "the alias stays")
}

func TestReleaseKeepsTheKeyAndItsAlias(t *testing.T) {
	fake := awsfake.NewKMS()
	k := newKeys(fake)
	created, _, err := k.ensure(context.Background(), ref, []string{replica})
	require.NoError(t, err)

	actions, err := k.release(context.Background(), ref)

	require.NoError(t, err)
	for _, region := range []string{home, replica} {
		key := mustKey(t, fake, region, created.ID)
		assert.Equal(t, map[string]string{"cpo-ci-run": "cpo-ci-test"}, key.Tags, region)
	}
	assert.Equal(t, types.KeyStateEnabled, mustKey(t, fake, home, created.ID).State)
	target, _ := fake.AliasTarget(home, wantAlias)
	assert.Equal(t, created.ID, target)
	assert.Equal(t, []string{eventReasonKeyReleased}, reasons(actions))
}

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

package rustfs

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// outputObjectStorageConfig is the output of the backup-storage section.
const outputObjectStorageConfig = "objectStorageConfig"

// The keys of the Secret of a request.
const (
	secretKeyAccessKeyID     = "accessKeyId"
	secretKeySecretAccessKey = "secretAccessKey"
)

// The events that the plugin records on a request.
const (
	eventReasonBucketCreated   = "BucketCreated"
	eventReasonPolicyCreated   = "PolicyCreated"
	eventReasonUserCreated     = "UserCreated"
	eventReasonAccessKeyIssued = "AccessKeyIssued"
	eventReasonPolicyAttached  = "PolicyAttached"
	eventReasonUserDeleted     = "UserDeleted"
	eventReasonPolicyDeleted   = "PolicyDeleted"
	eventReasonBucketDeleted   = "BucketDeleted"
	eventActionCreate          = "Create"
	eventActionUpdate          = "Update"
	eventActionDelete          = "Delete"
)

// sizeRequeue is the time after which a request reconciles again, so that
// its summary follows the used size of the bucket.
const sizeRequeue = 5 * time.Minute

// admin is what the plugin needs of the RustFS admin API. An error for a user
// or a policy that does not exist is ErrNotFound.
type admin interface {
	Ping(ctx context.Context) error
	UserInfo(ctx context.Context, accessKey string) (userInfo, error)
	AddUser(ctx context.Context, accessKey, secretKey string) error
	RemoveUser(ctx context.Context, accessKey string) error
	PolicyExists(ctx context.Context, name string) (bool, error)
	AddPolicy(ctx context.Context, name string, document []byte) error
	RemovePolicy(ctx context.Context, name string) error
	AttachPolicy(ctx context.Context, policy, user string) error
	BucketSizes(ctx context.Context) (map[string]uint64, error)
}

// buckets is what the plugin needs of the S3 API. Each method reports whether
// it changed the bucket.
type buckets interface {
	EnsureBucket(ctx context.Context, name string) (bool, error)
	DeleteBucket(ctx context.Context, name string) (bool, error)
}

// Params are the parameters of a request. The registration has none.
type Params struct{}

// Plugin serves the requests of the backup-storage section with a RustFS
// bucket. For each request it keeps one bucket, one user whose policy allows
// only that bucket, a Secret with the access key of the user, and an
// ObjectStorageConfig that names them. It finds each RustFS object by the
// name of the request, so a lost status write creates no second one.
type Plugin struct {
	settings Settings
	admin    admin
	buckets  buckets
	client   client.Client
	// reader reads the Secret of a request, which must be current: the
	// plugin reuses its key.
	reader client.Reader
}

// Reconcile creates the bucket, the policy, and the user of the request when
// they are missing, and returns the Secret and the ObjectStorageConfig. It
// keeps the key of the user while the Secret holds it, and sets a new key
// when the Secret is gone. A request that published its output already
// reports each object that it creates again, and a policy that it attaches
// again, as corrected drift. An error of RustFS is returned with the step
// that failed.
func (p *Plugin) Reconcile(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
) (pluginkit.Result, error) {
	name := resourceName(req.Object.Namespace, req.Object.Name)
	var changed repairs

	created, err := p.buckets.EnsureBucket(ctx, name)
	if err != nil {
		return pluginkit.Result{}, fmt.Errorf("creating the bucket %q: %w", name, err)
	}
	if created {
		changed.bucket = true
		req.Event(corev1.EventTypeNormal, eventReasonBucketCreated, eventActionCreate, "Created the bucket %s", name)
	}

	if changed.policy, err = p.ensurePolicy(ctx, req, name); err != nil {
		return pluginkit.Result{}, err
	}

	key, err := p.ensureUser(ctx, req, name, &changed)
	if err != nil {
		return pluginkit.Result{}, err
	}

	// Before the first output, a create is the first one, or the retry of a
	// reconcile that failed.
	var corrected []string
	if req.Object.Status.Outputs[outputObjectStorageConfig] != "" {
		corrected = changed.corrected(name)
	}

	size, sizeKnown, sizeErr := p.bucketSize(ctx, name)
	if sizeErr != nil {
		req.Log.Error(sizeErr, "Could not read the used size of the RustFS bucket", "bucket", name)
	}

	return pluginkit.Result{
		Outputs:      map[string]string{outputObjectStorageConfig: req.Object.Name},
		Objects:      []client.Object{credentialsSecret(req.Object.Name, name, key), p.contract(req.Object.Name, name)},
		Summary:      summary(name, p.settings.Endpoint, size, sizeKnown, sizeErr),
		Links:        links(p.settings.ConsoleURL),
		Resources:    resources(name, p.settings.Endpoint, req.Object.Name),
		RequeueAfter: sizeRequeue,
		Corrected:    corrected,
	}, nil
}

// repairs is what one reconcile created or attached in RustFS.
type repairs struct {
	bucket, policy, user, attached bool
}

// corrected names each repair of the RustFS objects name. A policy attached
// to a created user is part of the create.
func (c repairs) corrected(name string) []string {
	var out []string
	if c.bucket {
		out = append(out, "created the bucket "+name+", which was gone")
	}
	if c.policy {
		out = append(out, "created the policy "+name+", which was gone")
	}
	if c.user {
		out = append(out, "created the user "+name+", which was gone")
	}
	if c.attached && !c.user {
		out = append(out, "attached the policy "+name+" to the user "+name+" again")
	}
	return out
}

// ensurePolicy creates the policy name when it is missing, and reports
// whether it did.
func (p *Plugin) ensurePolicy(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	name string,
) (bool, error) {
	exists, err := p.admin.PolicyExists(ctx, name)
	if err != nil {
		return false, fmt.Errorf("reading the policy %q: %w", name, err)
	}

	if exists {
		return false, nil
	}

	if err := p.admin.AddPolicy(ctx, name, userPolicy(name)); err != nil {
		return false, fmt.Errorf("creating the policy %q: %w", name, err)
	}
	req.Event(corev1.EventTypeNormal, eventReasonPolicyCreated, eventActionCreate, "Created the policy %s", name)
	return true, nil
}

// ensureUser returns the secret key of the user name, whose policy is the
// policy name. It creates the user, or sets a new key, when the Secret of the
// request does not hold the key of the user. It sets the user and attached
// fields of changed.
func (p *Plugin) ensureUser(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	name string,
	changed *repairs,
) (string, error) {
	key, err := p.storedKey(ctx, req.Object, name)
	if err != nil {
		return "", err
	}

	info, err := p.admin.UserInfo(ctx, name)
	missing := errors.Is(err, ErrNotFound)
	if err != nil && !missing {
		return "", fmt.Errorf("reading the user %q: %w", name, err)
	}

	if missing || key == "" {
		if key == "" {
			if key, err = newSecretKey(); err != nil {
				return "", err
			}
		}

		if err := p.admin.AddUser(ctx, name, key); err != nil {
			return "", fmt.Errorf("setting the key of the user %q: %w", name, err)
		}
		if missing {
			changed.user = true
			req.Event(corev1.EventTypeNormal, eventReasonUserCreated, eventActionCreate, "Created the user %s", name)
		} else {
			req.Event(
				corev1.EventTypeNormal,
				eventReasonAccessKeyIssued,
				eventActionUpdate,
				"Set a new access key for the user %s, because its Secret was gone",
				name,
			)
		}
	}

	if missing || info.PolicyName != name {
		if err := p.admin.AttachPolicy(ctx, name, name); err != nil {
			return "", fmt.Errorf("attaching the policy %q to the user: %w", name, err)
		}
		changed.attached = true
		req.Event(
			corev1.EventTypeNormal,
			eventReasonPolicyAttached,
			eventActionUpdate,
			"Attached the policy %s to the user %s",
			name,
			name,
		)
	}

	return key, nil
}

// storedKey returns the secret key that the Secret of request holds for the
// user name, or "" when it holds none.
func (p *Plugin) storedKey(ctx context.Context, request *pluginsv1alpha1.PluginRequest, name string) (string, error) {
	var secret corev1.Secret
	key := client.ObjectKey{Namespace: request.Namespace, Name: secretName(request.Name)}
	err := p.reader.Get(ctx, key, &secret)
	switch {
	case apierrors.IsNotFound(err):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("reading the Secret %s: %w", key.Name, err)
	}

	if string(secret.Data[secretKeyAccessKeyID]) != name {
		return "", nil
	}
	return string(secret.Data[secretKeySecretAccessKey]), nil
}

// secretKeyLength is the longest secret key that RustFS accepts.
const secretKeyLength = 40

const keyAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func newSecretKey() (string, error) {
	key := make([]byte, secretKeyLength)
	limit := big.NewInt(int64(len(keyAlphabet)))
	for i := range key {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("generating a secret key: %w", err)
		}
		key[i] = keyAlphabet[n.Int64()]
	}
	return string(key), nil
}

// bucketSize returns the used size of the bucket name, and whether the RustFS
// scanner counted it yet.
func (p *Plugin) bucketSize(ctx context.Context, name string) (uint64, bool, error) {
	sizes, err := p.admin.BucketSizes(ctx)
	if err != nil {
		return 0, false, err
	}
	size, ok := sizes[name]
	return size, ok, nil
}

// credentialsSecret returns the Secret of the request with the access key of
// the user name.
func credentialsSecret(request, name, key string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName(request)},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			secretKeyAccessKeyID:     []byte(name),
			secretKeySecretAccessKey: []byte(key),
		},
	}
}

// contract returns the ObjectStorageConfig of the request, for the bucket and
// the Secret of the user name.
func (p *Plugin) contract(request, name string) *v1.ObjectStorageConfig {
	return &v1.ObjectStorageConfig{
		ObjectMeta: metav1.ObjectMeta{Name: request},
		Spec: v1.ObjectStorageConfigSpec{
			Type: v1.ObjectStorageTypeS3,
			S3: &v1.S3Storage{
				BucketName:     name,
				Region:         p.settings.region(),
				Endpoint:       p.settings.Endpoint,
				ForcePathStyle: true,
				Auth: v1.S3StorageAuth{
					Type: v1.ObjectStorageAuthTypeCredentials,
					Credentials: &v1.S3Credentials{SecretRef: v1.S3CredentialsSecretRef{
						Name:               secretName(request),
						AccessKeyIDKey:     secretKeyAccessKeyID,
						SecretAccessKeyKey: secretKeySecretAccessKey,
					}},
				},
			},
		},
	}
}

// Delete deletes the user, the policy, the objects and the bucket, the
// Secret, and the ObjectStorageConfig of the request. The user goes first, so
// that no key writes while the bucket empties. Each one that is already gone
// is skipped.
func (p *Plugin) Delete(ctx context.Context, req pluginkit.Request[Params, map[string]string]) error {
	name := resourceName(req.Object.Namespace, req.Object.Name)

	err := p.admin.RemoveUser(ctx, name)
	switch {
	case err == nil:
		req.Event(corev1.EventTypeNormal, eventReasonUserDeleted, eventActionDelete, "Deleted the user %s", name)
	case !errors.Is(err, ErrNotFound):
		return fmt.Errorf("deleting the user %q: %w", name, err)
	}

	exists, err := p.admin.PolicyExists(ctx, name)
	if err != nil {
		return fmt.Errorf("reading the policy %q: %w", name, err)
	}
	if exists {
		if err := p.admin.RemovePolicy(ctx, name); err != nil {
			return fmt.Errorf("deleting the policy %q: %w", name, err)
		}
		req.Event(corev1.EventTypeNormal, eventReasonPolicyDeleted, eventActionDelete, "Deleted the policy %s", name)
	}

	deleted, err := p.buckets.DeleteBucket(ctx, name)
	if err != nil {
		return fmt.Errorf("deleting the bucket %q: %w", name, err)
	}
	if deleted {
		req.Event(
			corev1.EventTypeNormal,
			eventReasonBucketDeleted,
			eventActionDelete,
			"Deleted the bucket %s and its objects",
			name,
		)
	}

	// The plugin deletes its own objects, because envtest has no garbage
	// collector.
	for _, o := range []struct {
		obj  client.Object
		name string
	}{
		{&corev1.Secret{}, secretName(req.Object.Name)},
		{&v1.ObjectStorageConfig{}, req.Object.Name},
	} {
		if err := p.deleteOwned(ctx, req.Object, o.obj, o.name); err != nil {
			return err
		}
	}
	return nil
}

// deleteOwned deletes the object objName of the kind of obj in the namespace
// of request when it carries the request label of request. An object of the
// name that the plugin did not write stays.
func (p *Plugin) deleteOwned(
	ctx context.Context,
	request *pluginsv1alpha1.PluginRequest,
	obj client.Object,
	objName string,
) error {
	err := p.reader.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: objName}, obj)
	switch {
	case apierrors.IsNotFound(err):
		return nil
	case err != nil:
		return fmt.Errorf("reading %T %q: %w", obj, objName, err)
	}

	labels := obj.GetLabels()
	if labels[pluginsv1alpha1.LabelRequest] != request.Name ||
		labels[pluginsv1alpha1.LabelRegistration] != request.Spec.Registration {
		return nil
	}

	uid := obj.GetUID()
	if err := p.client.Delete(ctx, obj, client.Preconditions{UID: &uid}); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("deleting %T %q: %w", obj, objName, err)
	}
	return nil
}

// Orphan keeps the bucket, the user, and the policy of the request. Nothing
// in RustFS ties them to the request, so nothing there changes.
func (p *Plugin) Orphan(context.Context, pluginkit.Request[Params, map[string]string]) error {
	return nil
}

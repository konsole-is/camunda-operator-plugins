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

package gatewayaccess

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func testClass() *gatewayv1.GatewayClass {
	return &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "example"},
		Spec:       gatewayv1.GatewayClassSpec{ControllerName: testController},
	}
}

func TestGatewayController_ReturnsTheControllerOfTheClass(t *testing.T) {
	reader := fakeReader(t, nil, testGateway(), testClass())

	controller, waiting, err := GatewayController(context.Background(), reader, validParams())
	require.NoError(t, err)
	assert.Nil(t, waiting)
	assert.Equal(t, testController, controller)
}

func TestGatewayController_MissingGatewayIsGatewayNotFound(t *testing.T) {
	reader := fakeReader(t, nil, testClass())

	controller, waiting, err := GatewayController(context.Background(), reader, validParams())
	require.NoError(t, err)
	assert.Empty(t, controller)
	require.NotNil(t, waiting)
	assert.Equal(t, ReasonGatewayNotFound, waiting.Reason)
	assert.Equal(t, "the Gateway gateway-system/public does not exist", waiting.Message)
}

func TestGatewayController_MissingClassIsGatewayNotFound(t *testing.T) {
	reader := fakeReader(t, nil, testGateway())

	_, waiting, err := GatewayController(context.Background(), reader, validParams())
	require.NoError(t, err)
	require.NotNil(t, waiting)
	assert.Equal(t, ReasonGatewayNotFound, waiting.Reason)
	assert.Equal(t, "the GatewayClass example of the Gateway gateway-system/public does not exist", waiting.Message)
}

func TestGatewayController_ForbiddenReadIsAnError(t *testing.T) {
	reader := fakeReader(t, &interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*gatewayv1.GatewayClass); ok {
				return apierrors.NewForbidden(schema.GroupResource{Resource: "gatewayclasses"}, key.Name, nil)
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}, testGateway(), testClass())

	_, waiting, err := GatewayController(context.Background(), reader, validParams())
	assert.Nil(t, waiting)
	require.Error(t, err)
	assert.True(t, apierrors.IsForbidden(err))
	assert.Contains(t, err.Error(), "reading the GatewayClass example of the Gateway gateway-system/public")
}

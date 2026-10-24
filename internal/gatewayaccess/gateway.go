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
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// GatewayController returns the controller name of the GatewayClass of the
// Gateway of p. Only the status entries of this controller tell whether the
// Gateway accepted a route. waiting has the reason GatewayNotFound when the
// Gateway or its GatewayClass does not exist.
func GatewayController(
	ctx context.Context,
	reader client.Reader,
	p Params,
) (controller gatewayv1.GatewayController, waiting *pluginkit.Waiting, err error) {
	var gateway gatewayv1.Gateway
	key := client.ObjectKey{Namespace: p.GatewayRef.Namespace, Name: p.GatewayRef.Name}
	if err := reader.Get(ctx, key, &gateway); apierrors.IsNotFound(err) {
		return "", gatewayNotFound("the Gateway %s does not exist", key), nil
	} else if err != nil {
		return "", nil, fmt.Errorf("reading the Gateway %s: %w", key, err)
	}

	var class gatewayv1.GatewayClass
	name := string(gateway.Spec.GatewayClassName)
	if err := reader.Get(ctx, client.ObjectKey{Name: name}, &class); apierrors.IsNotFound(err) {
		return "", gatewayNotFound("the GatewayClass %s of the Gateway %s does not exist", name, key), nil
	} else if err != nil {
		return "", nil, fmt.Errorf("reading the GatewayClass %s of the Gateway %s: %w", name, key, err)
	}

	return class.Spec.ControllerName, nil, nil
}

func gatewayNotFound(format string, args ...any) *pluginkit.Waiting {
	return &pluginkit.Waiting{Reason: ReasonGatewayNotFound, Message: fmt.Sprintf(format, args...)}
}

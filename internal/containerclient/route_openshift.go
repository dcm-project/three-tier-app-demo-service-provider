package containerclient

import (
	"context"
	"fmt"
	"time"

	routev1 "github.com/openshift/api/route/v1"
	routeclientset "github.com/openshift/client-go/route/clientset/versioned"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// openShiftRoutes creates and deletes OpenShift Route resources for the web tier.
// Used when SP_WEB_EXPOSURE=openshift so the public URL comes from the cluster router.
type openShiftRoutes struct {
	client    routeclientset.Interface
	namespace string
}

func newOpenShiftRoutes(kubeconfigPath, namespace string) (*openShiftRoutes, error) {
	if namespace == "" {
		return nil, fmt.Errorf("openshift route namespace is empty")
	}
	cfg, err := restConfigForRoutes(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("openshift kubeconfig: %w", err)
	}
	cs, err := routeclientset.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("openshift route client: %w", err)
	}
	return &openShiftRoutes{client: cs, namespace: namespace}, nil
}

func restConfigForRoutes(kubeconfigPath string) (*rest.Config, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		loadingRules.ExplicitPath = kubeconfigPath
	}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		&clientcmd.ConfigOverrides{},
	)
	return kubeConfig.ClientConfig()
}

func routeNameForStack(stackID string) string {
	return stackID + "-web"
}

func desiredWebRoute(stackID, namespace string) *routev1.Route {
	name := routeNameForStack(stackID)
	svcName := stackID + "-web"
	return &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "3-tier-demo-service-provider",
				"three-tier.stack":             stackID,
			},
		},
		Spec: routev1.RouteSpec{
			To: routev1.RouteTargetReference{
				Kind: "Service",
				Name: svcName,
			},
			Port: &routev1.RoutePort{
				TargetPort: intstr.FromInt(80),
			},
			TLS: &routev1.TLSConfig{
				Termination:                   routev1.TLSTerminationEdge,
				InsecureEdgeTerminationPolicy: routev1.InsecureEdgeTerminationPolicyRedirect,
			},
		},
	}
}

// ensureWebRoute creates or updates a Route to the web Service and returns a browser URL
// once status.ingress has a hostname (edge TLS → https).
func (r *openShiftRoutes) ensureWebRoute(ctx context.Context, stackID string) (*string, error) {
	name := routeNameForStack(stackID)
	rc := r.client.RouteV1().Routes(r.namespace)
	desired := desiredWebRoute(stackID, r.namespace)

	cur, err := rc.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if u := routePublicURL(cur); u != "" {
			out := u
			return &out, nil
		}
		cur.Spec = desired.Spec
		_, err = rc.Update(ctx, cur, metav1.UpdateOptions{})
		if err != nil {
			return nil, fmt.Errorf("update route %s: %w", name, err)
		}
	} else if apierrors.IsNotFound(err) {
		_, cerr := rc.Create(ctx, desired, metav1.CreateOptions{})
		if cerr != nil && !apierrors.IsAlreadyExists(cerr) {
			return nil, fmt.Errorf("create route %s: %w", name, cerr)
		}
	} else {
		return nil, fmt.Errorf("get route %s: %w", name, err)
	}

	deadline := time.Now().Add(3 * time.Minute)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timeout waiting for route %s ingress hostname", name)
		}
		cur, gerr := rc.Get(ctx, name, metav1.GetOptions{})
		if gerr != nil {
			return nil, fmt.Errorf("get route %s: %w", name, gerr)
		}
		if u := routePublicURL(cur); u != "" {
			out := u
			return &out, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func routePublicURL(r *routev1.Route) string {
	var host string
	if len(r.Status.Ingress) > 0 {
		host = r.Status.Ingress[0].Host
	}
	if host == "" {
		return ""
	}
	if r.Spec.TLS != nil {
		return "https://" + host
	}
	return "http://" + host
}

func (r *openShiftRoutes) deleteRoute(ctx context.Context, stackID string) error {
	name := routeNameForStack(stackID)
	err := r.client.RouteV1().Routes(r.namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete route %s: %w", name, err)
	}
	return nil
}

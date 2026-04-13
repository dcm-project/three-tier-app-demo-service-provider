package containerclient

import (
	"testing"

	routev1 "github.com/openshift/api/route/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestRouteNameForStack(t *testing.T) {
	t.Parallel()
	if got, want := routeNameForStack("pet1"), "pet1-web"; got != want {
		t.Errorf("routeNameForStack = %q, want %q", got, want)
	}
}

func TestDesiredWebRoute(t *testing.T) {
	t.Parallel()
	stackID, ns := "myapp", "prod-ns"
	r := desiredWebRoute(stackID, ns)
	if r.Name != "myapp-web" {
		t.Errorf("Name = %q, want myapp-web", r.Name)
	}
	if r.Namespace != ns {
		t.Errorf("Namespace = %q, want %q", r.Namespace, ns)
	}
	if r.Spec.To.Kind != "Service" || r.Spec.To.Name != "myapp-web" {
		t.Errorf("Spec.To = %+v, want Service myapp-web", r.Spec.To)
	}
	if r.Spec.Port == nil || r.Spec.Port.TargetPort != intstr.FromInt(80) {
		t.Errorf("Spec.Port.TargetPort = %+v, want 80", r.Spec.Port)
	}
	if r.Spec.TLS == nil || r.Spec.TLS.Termination != routev1.TLSTerminationEdge {
		t.Errorf("TLS edge termination missing: %+v", r.Spec.TLS)
	}
	if r.Labels["three-tier.stack"] != stackID {
		t.Errorf("labels[three-tier.stack] = %q", r.Labels["three-tier.stack"])
	}
}

func TestRoutePublicURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		r    *routev1.Route
		want string
	}{
		{
			name: "no ingress",
			r:    &routev1.Route{},
			want: "",
		},
		{
			name: "https edge",
			r: &routev1.Route{
				Spec: routev1.RouteSpec{TLS: &routev1.TLSConfig{}},
				Status: routev1.RouteStatus{
					Ingress: []routev1.RouteIngress{{Host: "pet.example.com"}},
				},
			},
			want: "https://pet.example.com",
		},
		{
			name: "http no TLS",
			r: &routev1.Route{
				Status: routev1.RouteStatus{
					Ingress: []routev1.RouteIngress{{Host: "plain.example.com"}},
				},
			},
			want: "http://plain.example.com",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := routePublicURL(tt.r); got != tt.want {
				t.Errorf("routePublicURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

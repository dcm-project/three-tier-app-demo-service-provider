package containerclient

import (
	routev1 "github.com/openshift/api/route/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("OpenShift route helpers", func() {
	Describe("routeNameForStack", func() {
		It("returns stack id with -web suffix", func() {
			Expect(routeNameForStack("pet1")).To(Equal("pet1-web"))
		})
	})

	Describe("desiredWebRoute", func() {
		It("builds edge TLS route to the web Service", func() {
			stackID, ns, svcName := "myapp", "prod-ns", "web-svc-abc12"
			r := desiredWebRoute(stackID, ns, svcName)
			Expect(r.Name).To(Equal("myapp-web"))
			Expect(r.Namespace).To(Equal(ns))
			Expect(r.Spec.To.Kind).To(Equal("Service"))
			Expect(r.Spec.To.Name).To(Equal(svcName))
			Expect(r.Spec.Port).NotTo(BeNil())
			Expect(r.Spec.Port.TargetPort).To(Equal(intstr.FromInt(webPortDefault)))
			Expect(r.Spec.TLS).NotTo(BeNil())
			Expect(r.Spec.TLS.Termination).To(Equal(routev1.TLSTerminationEdge))
			Expect(r.Labels["three-tier.stack"]).To(Equal(stackID))
		})
	})

	Describe("routePublicURL", func() {
		DescribeTable("derived URL",
			func(r *routev1.Route, want string) {
				Expect(routePublicURL(r)).To(Equal(want))
			},
			Entry("no ingress", &routev1.Route{}, ""),
			Entry("https edge", &routev1.Route{
				Spec: routev1.RouteSpec{TLS: &routev1.TLSConfig{}},
				Status: routev1.RouteStatus{
					Ingress: []routev1.RouteIngress{{Host: "pet.example.com"}},
				},
			}, "https://pet.example.com"),
			Entry("http without TLS", &routev1.Route{
				Status: routev1.RouteStatus{
					Ingress: []routev1.RouteIngress{{Host: "plain.example.com"}},
				},
			}, "http://plain.example.com"),
		)
	})
})

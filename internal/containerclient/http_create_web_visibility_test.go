package containerclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/config"
	k8sapi "github.com/dcm-project/k8s-container-service-provider/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CreateContainers web tier port visibility", func() {
	var (
		ctx     context.Context
		spec    v1alpha1.ThreeTierSpec
		stackDB config.StackDBCfg
	)

	BeforeEach(func() {
		ctx = context.Background()
		spec = v1alpha1.ThreeTierSpec{
			Database: v1alpha1.DatabaseTierSpec{Engine: "postgres", Version: "15"},
			App:      v1alpha1.AppTierSpec{Image: "spring-petclinic:latest"},
			Web:      v1alpha1.WebTierSpec{Image: "nginx:alpine"},
		}
		stackDB = config.StackDBCfg{
			Password:     "petclinic",
			DatabaseName: "petclinic",
			PostgresUser: "postgres",
			MysqlUser:    "root",
		}
	})

	It("uses internal web service on OpenShift", func() {
		srv, bodies, decodeErr, cleanup := newCaptureCreateBodiesServer()
		defer cleanup()
		// Non-nil stub: openshift exposure requires a route client in newHTTPClient; CreateContainers
		// does not call the Route API (only visibility differs).
		h, err := newHTTPClient(srv.URL, stackDB, config.WebExposureOpenShift, &openShiftRoutes{namespace: "test"})
		Expect(err).NotTo(HaveOccurred())
		Expect(h.CreateContainers(ctx, "visos", spec)).To(Succeed())
		Expect(*decodeErr).NotTo(HaveOccurred())

		web := findCreateBodyForName(bodies, "visos-web")
		Expect(web).NotTo(BeNil())
		Expect(web.Spec.Network).NotTo(BeNil())
		Expect(web.Spec.Network.Ports).NotTo(BeNil())
		ports := *web.Spec.Network.Ports
		Expect(ports).NotTo(BeEmpty())
		Expect(ports[0].Visibility).To(Equal(k8sapi.Internal))
	})

	It("uses external web service on Kubernetes", func() {
		srv, bodies, decodeErr, cleanup := newCaptureCreateBodiesServer()
		defer cleanup()
		h, err := newHTTPClient(srv.URL, stackDB, config.WebExposureKubernetes, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(h.CreateContainers(ctx, "visk8s", spec)).To(Succeed())
		Expect(*decodeErr).NotTo(HaveOccurred())

		web := findCreateBodyForName(bodies, "visk8s-web")
		Expect(web).NotTo(BeNil())
		Expect(web.Spec.Network).NotTo(BeNil())
		Expect(web.Spec.Network.Ports).NotTo(BeNil())
		ports := *web.Spec.Network.Ports
		Expect(ports).NotTo(BeEmpty())
		Expect(ports[0].Visibility).To(Equal(k8sapi.External))
	})
})

// newCaptureCreateBodiesServer records each POST /api/v1alpha1/containers JSON body
// and returns 201 with a minimal container JSON (enough for the generated client).
func newCaptureCreateBodiesServer() (srv *httptest.Server, bodies *[]k8sapi.Container, decodeErr *error, cleanup func()) {
	var captured []k8sapi.Container
	var decErr error
	decodeErr = &decErr
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1alpha1/containers" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var body k8sapi.Container
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			decErr = err
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		captured = append(captured, body)
		id := r.URL.Query().Get("id")
		now := time.Now()
		st := k8sapi.RUNNING
		resp := k8sapi.Container{
			Id:         &id,
			Status:     &st,
			CreateTime: &now,
			UpdateTime: &now,
			Spec: k8sapi.ContainerSpec{
				ServiceType: k8sapi.ContainerSpecServiceTypeContainer,
				Metadata:    body.Spec.Metadata,
				Image:       body.Spec.Image,
				Resources:   body.Spec.Resources,
				Network:     body.Spec.Network,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	return srv, &captured, decodeErr, func() { srv.Close() }
}

func findCreateBodyForName(bodies *[]k8sapi.Container, name string) *k8sapi.Container {
	for i := range *bodies {
		b := &(*bodies)[i]
		if b.Spec.Metadata.Name == name {
			return b
		}
	}
	return nil
}

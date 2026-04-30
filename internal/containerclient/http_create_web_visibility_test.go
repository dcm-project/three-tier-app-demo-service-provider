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
		srv, reqs, decodeErr, cleanup := newCaptureCreateBodiesServer()
		defer cleanup()
		// Non-nil stub: openshift exposure requires a route client in newHTTPClient; CreateContainers
		// does not call the Route API (only visibility differs).
		h, err := newHTTPClient(srv.URL, stackDB, config.WebExposureOpenShift, &openShiftRoutes{namespace: "test"})
		Expect(err).NotTo(HaveOccurred())
		Expect(h.CreateContainers(ctx, "visos", spec)).To(Succeed())
		Expect(*decodeErr).NotTo(HaveOccurred())

		web := findCreateBodyForID(reqs, "visos-web")
		Expect(web).NotTo(BeNil())
		Expect(web.Spec.Metadata.Name).To(Equal("web-visos"))
		Expect(web.Spec.Network).NotTo(BeNil())
		Expect(web.Spec.Network.Ports).NotTo(BeNil())
		ports := *web.Spec.Network.Ports
		Expect(ports).NotTo(BeEmpty())
		Expect(ports[0].Visibility).To(Equal(k8sapi.Internal))
	})

	It("uses external web service on Kubernetes", func() {
		srv, reqs, decodeErr, cleanup := newCaptureCreateBodiesServer()
		defer cleanup()
		h, err := newHTTPClient(srv.URL, stackDB, config.WebExposureKubernetes, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(h.CreateContainers(ctx, "visk8s", spec)).To(Succeed())
		Expect(*decodeErr).NotTo(HaveOccurred())

		web := findCreateBodyForID(reqs, "visk8s-web")
		Expect(web).NotTo(BeNil())
		Expect(web.Spec.Metadata.Name).To(Equal("web-visk8s"))
		Expect(web.Spec.Network).NotTo(BeNil())
		Expect(web.Spec.Network.Ports).NotTo(BeNil())
		ports := *web.Spec.Network.Ports
		Expect(ports).NotTo(BeEmpty())
		Expect(ports[0].Visibility).To(Equal(k8sapi.External))
	})
})

// newCaptureCreateBodiesServer records each POST /api/v1alpha1/containers JSON body
// and returns 201 with a minimal container JSON (enough for the generated client).
type capturedCreateRequest struct {
	id   string
	body k8sapi.Container
}

func newCaptureCreateBodiesServer() (srv *httptest.Server, reqs *[]capturedCreateRequest, decodeErr *error, cleanup func()) {
	var captured []capturedCreateRequest
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
		id := r.URL.Query().Get("id")
		captured = append(captured, capturedCreateRequest{id: id, body: body})
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

func findCreateBodyForID(reqs *[]capturedCreateRequest, id string) *k8sapi.Container {
	for i := range *reqs {
		r := &(*reqs)[i]
		if r.id == id {
			return &r.body
		}
	}
	return nil
}

var _ = Describe("CreateContainers metadata name", func() {
	It("caps metadata.name so GenerateName suffix still fits", func() {
		srv, reqs, decodeErr, cleanup := newCaptureCreateBodiesServer()
		defer cleanup()

		stackDB := config.StackDBCfg{
			Password:     "petclinic",
			DatabaseName: "petclinic",
			PostgresUser: "postgres",
			MysqlUser:    "root",
		}
		h, err := newHTTPClient(srv.URL, stackDB, config.WebExposureKubernetes, nil)
		Expect(err).NotTo(HaveOccurred())

		spec := v1alpha1.ThreeTierSpec{
			Database: v1alpha1.DatabaseTierSpec{Engine: "postgres", Version: "15"},
			App:      v1alpha1.AppTierSpec{Image: "spring-petclinic:latest"},
			Web:      v1alpha1.WebTierSpec{Image: "nginx:alpine"},
		}
		stackID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		Expect(h.CreateContainers(context.Background(), stackID, spec)).To(Succeed())
		Expect(*decodeErr).NotTo(HaveOccurred())

		web := findCreateBodyForID(reqs, stackID+"-web")
		Expect(web).NotTo(BeNil())
		Expect(len(web.Spec.Metadata.Name)).To(Equal(k8sContainerMetadataNameMaxLen))
		Expect(web.Spec.Metadata.Name).To(HavePrefix("web-"))
		Expect(web.Spec.Metadata.Name).NotTo(HaveSuffix("-"))
	})
})

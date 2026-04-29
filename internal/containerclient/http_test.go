package containerclient

import (
	"context"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/config"
	k8sapi "github.com/dcm-project/k8s-container-service-provider/api/v1alpha1"
)

func testStackDB() config.StackDBCfg {
	return config.StackDBCfg{
		Password:     "petclinic",
		DatabaseName: "petclinic",
		PostgresUser: "postgres",
		MysqlUser:    "root",
	}
}

func springDatasourceURL(c *k8sapi.Container) string {
	if c == nil || c.Spec.Process == nil || c.Spec.Process.Env == nil {
		return ""
	}
	for _, e := range *c.Spec.Process.Env {
		if e.Name == "SPRING_DATASOURCE_URL" {
			return e.Value
		}
	}
	return ""
}

func webNginxScript(c *k8sapi.Container) string {
	if c == nil || c.Spec.Process == nil || c.Spec.Process.Args == nil {
		return ""
	}
	args := *c.Spec.Process.Args
	if len(args) < 1 {
		return ""
	}
	return args[0]
}

var _ = Describe("CreateContainers cluster IP propagation", func() {
	It("sets app JDBC to the db Service name and web proxy_pass to the app Service name", func() {
		srv, reqs, decodeErr, cleanup := newCaptureCreateBodiesServer()
		defer cleanup()
		h, err := newHTTPClient(srv.URL, testStackDB(), config.WebExposureKubernetes, nil)
		Expect(err).NotTo(HaveOccurred())

		spec := v1alpha1.ThreeTierSpec{
			Database: v1alpha1.DatabaseTierSpec{Engine: "postgres", Version: "15"},
			App:      v1alpha1.AppTierSpec{Image: "spring-petclinic:latest"},
			Web:      v1alpha1.WebTierSpec{Image: "nginx:alpine"},
		}
		stackID := "ipchain"
		Expect(h.CreateContainers(context.Background(), stackID, spec)).To(Succeed())
		Expect(*decodeErr).NotTo(HaveOccurred())

		app := findCreateBodyForID(reqs, stackID+"-app")
		Expect(app).NotTo(BeNil())
		Expect(springDatasourceURL(app)).To(
			Equal("jdbc:postgresql://" + testMockServiceNameDB + ":5432/petclinic"),
		)

		web := findCreateBodyForID(reqs, stackID+"-web")
		Expect(web).NotTo(BeNil())
		Expect(webNginxScript(web)).To(ContainSubstring("proxy_pass http://" + testMockServiceNameApp + ":8080;"))
	})

	It("uses MySQL JDBC and reflects custom app HTTP port in nginx proxy_pass", func() {
		srv, reqs, decodeErr, cleanup := newCaptureCreateBodiesServer()
		defer cleanup()
		h, err := newHTTPClient(srv.URL, testStackDB(), config.WebExposureKubernetes, nil)
		Expect(err).NotTo(HaveOccurred())
		p := 9090
		spec := v1alpha1.ThreeTierSpec{
			Database: v1alpha1.DatabaseTierSpec{Engine: "mysql", Version: "8"},
			App:      v1alpha1.AppTierSpec{Image: "spring-petclinic:latest", HttpPort: &p},
			Web:      v1alpha1.WebTierSpec{Image: "nginx:alpine"},
		}
		stackID := "ipchain-mysql"
		Expect(h.CreateContainers(context.Background(), stackID, spec)).To(Succeed())
		Expect(*decodeErr).NotTo(HaveOccurred())

		app := findCreateBodyForID(reqs, stackID+"-app")
		Expect(springDatasourceURL(app)).To(
			Equal("jdbc:mysql://" + testMockServiceNameDB + ":3306/petclinic"),
		)

		web := findCreateBodyForID(reqs, stackID+"-web")
		Expect(webNginxScript(web)).To(ContainSubstring("proxy_pass http://" + testMockServiceNameApp + ":9090;"))
	})
})

var _ = Describe("HTTPClient", func() {
	var (
		srv    *httptest.Server
		client *HTTPClient
		ctx    context.Context
	)

	BeforeEach(func() {
		var err error
		srv = MockContainerServer()
		client, err = newHTTPClient(srv.URL, testStackDB(), config.WebExposureKubernetes, nil)
		Expect(err).NotTo(HaveOccurred())
		ctx = context.Background()
	})

	AfterEach(func() {
		srv.Close()
	})

	Describe("CreateContainers", func() {
		var spec v1alpha1.ThreeTierSpec

		BeforeEach(func() {
			spec = v1alpha1.ThreeTierSpec{
				Database: v1alpha1.DatabaseTierSpec{Engine: "postgres", Version: "15"},
				App:      v1alpha1.AppTierSpec{Image: "spring-petclinic:latest"},
				Web:      v1alpha1.WebTierSpec{Image: "nginx:alpine"},
			}
		})

		It("creates three containers successfully", func() {
			Expect(client.CreateContainers(ctx, "stack1", spec)).To(Succeed())
		})

		It("returns ErrConflict when containers already exist", func() {
			Expect(client.CreateContainers(ctx, "stack1", spec)).To(Succeed())
			Expect(client.CreateContainers(ctx, "stack1", spec)).To(MatchError(ErrConflict))
		})

		It("includes k8s SP problem detail when create returns HTTP 400", func() {
			err := client.CreateContainers(ctx, "mock-400", spec)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(And(
				ContainSubstring("create db:"),
				ContainSubstring("unexpected status 400"),
				ContainSubstring("mock create rejected (400)"),
			))
		})

		It("includes k8s SP problem detail when create returns HTTP 500", func() {
			err := client.CreateContainers(ctx, "mock-500", spec)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(And(
				ContainSubstring("create db:"),
				ContainSubstring("unexpected status 500"),
				ContainSubstring("mock create failed (500)"),
			))
		})
	})

	Describe("DeleteContainers", func() {
		It("deletes containers successfully", func() {
			spec := v1alpha1.ThreeTierSpec{
				Database: v1alpha1.DatabaseTierSpec{Engine: "postgres", Version: "15"},
				App:      v1alpha1.AppTierSpec{Image: "spring-petclinic:latest"},
				Web:      v1alpha1.WebTierSpec{Image: "nginx:alpine"},
			}
			Expect(client.CreateContainers(ctx, "stack1", spec)).To(Succeed())

			Expect(client.DeleteContainers(ctx, "stack1")).To(Succeed())
		})

		It("succeeds even when containers are not found (best-effort delete)", func() {
			// 404 is treated as acceptable: allows partial rollback without
			// leaving the caller in an inconsistent state (ygalblum).
			Expect(client.DeleteContainers(ctx, "nonexistent")).To(Succeed())
		})
	})

	Describe("CreateContainers with custom port specs", func() {
		It("accepts tier specs with explicit ports", func() {
			dbPorts := []v1alpha1.ContainerPort{{ContainerPort: 5433}}
			appPorts := []v1alpha1.ContainerPort{{ContainerPort: 9090}, {ContainerPort: 9091}}
			spec := v1alpha1.ThreeTierSpec{
				Database: v1alpha1.DatabaseTierSpec{
					Engine:  "postgres",
					Version: "15",
					Network: &v1alpha1.TierNetwork{Ports: &dbPorts},
				},
				App: v1alpha1.AppTierSpec{
					Image:   "app:latest",
					Network: &v1alpha1.TierNetwork{Ports: &appPorts},
				},
				Web: v1alpha1.WebTierSpec{Image: "nginx:alpine"},
			}
			Expect(client.CreateContainers(ctx, "stack2", spec)).To(Succeed())
		})
	})
})

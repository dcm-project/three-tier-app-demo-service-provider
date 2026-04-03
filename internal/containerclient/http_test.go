package containerclient_test

import (
	"context"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/config"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/containerclient"
)

func testStackDB() config.StackDBConfig {
	return config.StackDBConfig{
		Password:     "petclinic",
		DatabaseName: "petclinic",
		PostgresUser: "postgres",
		MysqlUser:    "root",
	}
}

var _ = Describe("HTTPClient", func() {
	var (
		srv    *httptest.Server
		client *containerclient.HTTPClient
		ctx    context.Context
	)

	BeforeEach(func() {
		var err error
		srv = containerclient.MockContainerServer()
		client, err = containerclient.NewHTTPClient(srv.URL, testStackDB())
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

		It("creates three containers and returns correct IDs", func() {
			dbID, appID, webID, err := client.CreateContainers(ctx, "stack1", spec)
			Expect(err).NotTo(HaveOccurred())
			Expect(dbID).To(Equal("stack1-db"))
			Expect(appID).To(Equal("stack1-app"))
			Expect(webID).To(Equal("stack1-web"))
		})

		It("returns ErrConflict when containers already exist", func() {
			_, _, _, err := client.CreateContainers(ctx, "stack1", spec)
			Expect(err).NotTo(HaveOccurred())

			_, _, _, err = client.CreateContainers(ctx, "stack1", spec)
			Expect(err).To(MatchError(containerclient.ErrConflict))
		})
	})

	Describe("DeleteContainers", func() {
		It("deletes containers successfully", func() {
			spec := v1alpha1.ThreeTierSpec{
				Database: v1alpha1.DatabaseTierSpec{Engine: "postgres", Version: "15"},
				App:      v1alpha1.AppTierSpec{Image: "spring-petclinic:latest"},
				Web:      v1alpha1.WebTierSpec{Image: "nginx:alpine"},
			}
			_, _, _, err := client.CreateContainers(ctx, "stack1", spec)
			Expect(err).NotTo(HaveOccurred())

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
			_, _, _, err := client.CreateContainers(ctx, "stack2", spec)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})

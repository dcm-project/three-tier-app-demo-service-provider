package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/api/server"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/config"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/containerclient"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/handlers"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/service"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/store"
	"github.com/go-chi/chi/v5"
)

// statusOverrideClient wraps MockClient and overrides GetStatus for testing status changes.
type statusOverrideClient struct {
	inner    *containerclient.MockClient
	override map[string]v1alpha1.ThreeTierAppStatus
	mu       sync.RWMutex
}

func (c *statusOverrideClient) CreateContainers(ctx context.Context, stackID string, spec v1alpha1.ThreeTierSpec) error {
	return c.inner.CreateContainers(ctx, stackID, spec)
}

func (c *statusOverrideClient) DeleteContainers(ctx context.Context, stackID string) error {
	return c.inner.DeleteContainers(ctx, stackID)
}

func (c *statusOverrideClient) GetStatus(ctx context.Context, stackID string) (v1alpha1.ThreeTierAppStatus, bool) {
	c.mu.RLock()
	s, ok := c.override[stackID]
	c.mu.RUnlock()
	if ok {
		return s, true
	}
	return c.inner.GetStatus(ctx, stackID)
}

func (c *statusOverrideClient) GetWebEndpoint(ctx context.Context, stackID string) *string {
	return c.inner.GetWebEndpoint(ctx, stackID)
}

func (c *statusOverrideClient) setStatus(stackID string, status v1alpha1.ThreeTierAppStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.override == nil {
		c.override = make(map[string]v1alpha1.ThreeTierAppStatus)
	}
	c.override[stackID] = status
}

// mockStatusReporter records Publish and PublishDeleted calls for assertions.
type mockStatusReporter struct {
	mu      sync.Mutex
	publish []publishCall
	deleted []string
}

type publishCall struct {
	InstanceID string
	Status     string
	Message    string
}

func (m *mockStatusReporter) Publish(ctx context.Context, instanceID, status, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.publish = append(m.publish, publishCall{InstanceID: instanceID, Status: status, Message: message})
}

func (m *mockStatusReporter) PublishDeleted(ctx context.Context, instanceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted = append(m.deleted, instanceID)
}

func (m *mockStatusReporter) getPublishCalls() []publishCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]publishCall, len(m.publish))
	copy(out, m.publish)
	return out
}

func (m *mockStatusReporter) getDeletedCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.deleted))
	copy(out, m.deleted)
	return out
}

func newTestStore() store.AppStore {
	f, err := os.CreateTemp("", "handlers-test-*.db")
	Expect(err).NotTo(HaveOccurred())
	path := f.Name()
	Expect(f.Close()).To(Succeed())
	DeferCleanup(func() { _ = os.Remove(path) })

	st, err := store.New(config.StoreConfig{Type: "sqlite", Path: path}, "")
	Expect(err).NotTo(HaveOccurred())
	return st
}

func TestHandlers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Handlers Suite")
}

var _ = Describe("Handlers with MockClient and status reporting", func() {
	var (
		srv      *httptest.Server
		reporter *mockStatusReporter
	)

	BeforeEach(func() {
		reporter = &mockStatusReporter{}
		svc := service.New(newTestStore(), &containerclient.MockClient{}, reporter)
		h := &handlers.Handlers{Svc: svc}
		r := chi.NewRouter()
		_ = server.HandlerFromMuxWithBaseURL(server.NewStrictHandler(h, nil), r, "/api/v1alpha1")
		srv = httptest.NewServer(r)
	})

	AfterEach(func() {
		if srv != nil {
			srv.Close()
		}
	})

	It("accepts database engine+version (Pet Clinic catalog), SP derives OCI image", func() {
		req := v1alpha1.ThreeTierApp{
			Metadata: v1alpha1.ThreeTierAppMetadata{Name: "db-engine-version-stack"},
			Spec: v1alpha1.ThreeTierSpec{
				Database: v1alpha1.DatabaseTierSpec{Engine: "mysql", Version: "8"},
				App:      v1alpha1.AppTierSpec{Image: "app:latest"},
				Web:      v1alpha1.WebTierSpec{Image: "nginx:alpine"},
			},
		}
		body, _ := json.Marshal(req)
		resp, err := http.Post(srv.URL+"/api/v1alpha1/three-tier-apps", "application/json", bytes.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusCreated))

		var stack v1alpha1.ThreeTierApp
		Expect(json.NewDecoder(resp.Body).Decode(&stack)).To(Succeed())
		Expect(stack.Spec.Database.Engine).To(Equal("mysql"))
		Expect(stack.Spec.Database.Version).To(Equal("8"))
	})

	It("returns status PENDING on create (provisioning is async)", func() {
		req := v1alpha1.ThreeTierApp{
			Metadata: v1alpha1.ThreeTierAppMetadata{Name: "test-stack"},
			Spec: v1alpha1.ThreeTierSpec{
				Database: v1alpha1.DatabaseTierSpec{Engine: "postgres", Version: "16"},
				App:      v1alpha1.AppTierSpec{Image: "app:latest"},
				Web:      v1alpha1.WebTierSpec{Image: "nginx:alpine"},
			},
		}
		body, _ := json.Marshal(req)
		resp, err := http.Post(srv.URL+"/api/v1alpha1/three-tier-apps", "application/json", bytes.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusCreated))

		var stack v1alpha1.ThreeTierApp
		Expect(json.NewDecoder(resp.Body).Decode(&stack)).To(Succeed())
		Expect(stack.Status).To(HaveValue(Equal(v1alpha1.PENDING)))

		// Provisioning goroutine publishes RUNNING asynchronously.
		Eventually(reporter.getPublishCalls, "15s", "25ms").Should(ContainElement(
			WithTransform(func(c publishCall) string { return c.Status }, Equal("RUNNING")),
		))
	})

	It("returns live container status on GET", func() {
		req := v1alpha1.ThreeTierApp{
			Metadata: v1alpha1.ThreeTierAppMetadata{Name: "get-status-stack"},
			Spec: v1alpha1.ThreeTierSpec{
				Database: v1alpha1.DatabaseTierSpec{Engine: "postgres", Version: "16"},
				App:      v1alpha1.AppTierSpec{Image: "app:latest"},
				Web:      v1alpha1.WebTierSpec{Image: "nginx:alpine"},
			},
		}
		body, _ := json.Marshal(req)
		postResp, err := http.Post(srv.URL+"/api/v1alpha1/three-tier-apps", "application/json", bytes.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		Expect(postResp.StatusCode).To(Equal(http.StatusCreated))
		_ = postResp.Body.Close()

		// Wait for provisioning goroutine to register containers (makes GetStatus return RUNNING).
		Eventually(func() v1alpha1.ThreeTierAppStatus {
			resp, err := http.Get(srv.URL + "/api/v1alpha1/three-tier-apps/get-status-stack")
			if err != nil || resp.StatusCode != http.StatusOK {
				if resp != nil {
					_ = resp.Body.Close()
				}
				return v1alpha1.PENDING
			}
			defer resp.Body.Close()
			var stack v1alpha1.ThreeTierApp
			if err := json.NewDecoder(resp.Body).Decode(&stack); err != nil || stack.Status == nil {
				return v1alpha1.PENDING
			}
			return *stack.Status
		}, "15s", "25ms").Should(Equal(v1alpha1.RUNNING))
	})

	It("deletes a 3-tier app (no status event published)", func() {
		req := v1alpha1.ThreeTierApp{
			Metadata: v1alpha1.ThreeTierAppMetadata{Name: "del-stack"},
			Spec: v1alpha1.ThreeTierSpec{
				Database: v1alpha1.DatabaseTierSpec{Engine: "postgres", Version: "16"},
				App:      v1alpha1.AppTierSpec{Image: "app:latest"},
				Web:      v1alpha1.WebTierSpec{Image: "nginx:alpine"},
			},
		}
		body, _ := json.Marshal(req)
		postResp, err := http.Post(srv.URL+"/api/v1alpha1/three-tier-apps", "application/json", bytes.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		Expect(postResp.StatusCode).To(Equal(http.StatusCreated))
		_ = postResp.Body.Close()

		// Wait for provisioning so the entry exists in the store before deleting.
		Eventually(func() int {
			resp, err := http.Get(srv.URL + "/api/v1alpha1/three-tier-apps/del-stack")
			if err != nil || resp == nil {
				return 0
			}
			code := resp.StatusCode
			_ = resp.Body.Close()
			return code
		}, "15s", "25ms").Should(Equal(http.StatusOK))

		delReq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1alpha1/three-tier-apps/del-stack", nil)
		resp, err := http.DefaultClient.Do(delReq)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusNoContent))

		Expect(reporter.getDeletedCalls()).To(BeEmpty())
	})

	It("returns status from MockClient on List", func() {
		req := v1alpha1.ThreeTierApp{
			Metadata: v1alpha1.ThreeTierAppMetadata{Name: "list-stack"},
			Spec: v1alpha1.ThreeTierSpec{
				Database: v1alpha1.DatabaseTierSpec{Engine: "postgres", Version: "16"},
				App:      v1alpha1.AppTierSpec{Image: "app:latest"},
				Web:      v1alpha1.WebTierSpec{Image: "nginx:alpine"},
			},
		}
		body, _ := json.Marshal(req)
		postResp, err := http.Post(srv.URL+"/api/v1alpha1/three-tier-apps", "application/json", bytes.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		Expect(postResp.StatusCode).To(Equal(http.StatusCreated))
		_ = postResp.Body.Close()

		// Wait for provisioning goroutine so GetStatus returns RUNNING (async; allow CI headroom).
		Eventually(func() v1alpha1.ThreeTierAppStatus {
			resp, err := http.Get(srv.URL + "/api/v1alpha1/three-tier-apps")
			if err != nil || resp.StatusCode != http.StatusOK {
				return v1alpha1.PENDING
			}
			defer resp.Body.Close()
			var list v1alpha1.ThreeTierAppList
			if err := json.NewDecoder(resp.Body).Decode(&list); err != nil ||
				list.ThreeTierApps == nil {
				return v1alpha1.PENDING
			}
			for _, a := range *list.ThreeTierApps {
				if a.Id != nil && *a.Id == "list-stack" && a.Status != nil {
					return *a.Status
				}
			}
			return v1alpha1.PENDING
		}, "15s", "25ms").Should(Equal(v1alpha1.RUNNING))
	})
})

var _ = Describe("Handlers status consistency (configurable client)", func() {
	var (
		srv      *httptest.Server
		reporter *mockStatusReporter
		client   *statusOverrideClient
	)

	BeforeEach(func() {
		reporter = &mockStatusReporter{}
		client = &statusOverrideClient{inner: &containerclient.MockClient{}}
		svc := service.New(newTestStore(), client, reporter)
		h := &handlers.Handlers{Svc: svc}
		r := chi.NewRouter()
		_ = server.HandlerFromMuxWithBaseURL(server.NewStrictHandler(h, nil), r, "/api/v1alpha1")
		srv = httptest.NewServer(r)
	})

	AfterEach(func() {
		if srv != nil {
			srv.Close()
		}
	})

	It("returns FAILED when container status is FAILED", func() {
		req := v1alpha1.ThreeTierApp{
			Metadata: v1alpha1.ThreeTierAppMetadata{Name: "fail-stack"},
			Spec: v1alpha1.ThreeTierSpec{
				Database: v1alpha1.DatabaseTierSpec{Engine: "postgres", Version: "16"},
				App:      v1alpha1.AppTierSpec{Image: "app:latest"},
				Web:      v1alpha1.WebTierSpec{Image: "nginx:alpine"},
			},
		}
		body, _ := json.Marshal(req)
		postResp, err := http.Post(srv.URL+"/api/v1alpha1/three-tier-apps", "application/json", bytes.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		Expect(postResp.StatusCode).To(Equal(http.StatusCreated))
		_ = postResp.Body.Close()

		// Wait until provisioning completes (RUNNING). GET is 200 while still PENDING;
		// setting FAILED early would make waitForRunning error and delete the row.
		Eventually(func() v1alpha1.ThreeTierAppStatus {
			resp, err := http.Get(srv.URL + "/api/v1alpha1/three-tier-apps/fail-stack")
			if err != nil || resp.StatusCode != http.StatusOK {
				if resp != nil {
					_ = resp.Body.Close()
				}
				return v1alpha1.PENDING
			}
			defer resp.Body.Close()
			var stack v1alpha1.ThreeTierApp
			if err := json.NewDecoder(resp.Body).Decode(&stack); err != nil || stack.Status == nil {
				return v1alpha1.PENDING
			}
			return *stack.Status
		}, "15s", "25ms").Should(Equal(v1alpha1.RUNNING))

		client.setStatus("fail-stack", v1alpha1.FAILED)

		resp, err := http.Get(srv.URL + "/api/v1alpha1/three-tier-apps/fail-stack")
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		var stack v1alpha1.ThreeTierApp
		Expect(json.NewDecoder(resp.Body).Decode(&stack)).To(Succeed())
		Expect(stack.Status).To(HaveValue(Equal(v1alpha1.FAILED)))
	})

	It("returns PENDING when container status is PENDING", func() {
		req := v1alpha1.ThreeTierApp{
			Metadata: v1alpha1.ThreeTierAppMetadata{Name: "pending-stack"},
			Spec: v1alpha1.ThreeTierSpec{
				Database: v1alpha1.DatabaseTierSpec{Engine: "postgres", Version: "16"},
				App:      v1alpha1.AppTierSpec{Image: "app:latest"},
				Web:      v1alpha1.WebTierSpec{Image: "nginx:alpine"},
			},
		}
		body, _ := json.Marshal(req)
		postResp, err := http.Post(srv.URL+"/api/v1alpha1/three-tier-apps", "application/json", bytes.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		Expect(postResp.StatusCode).To(Equal(http.StatusCreated))
		_ = postResp.Body.Close()

		// Wait until RUNNING before overriding; see fail-stack test comment.
		Eventually(func() v1alpha1.ThreeTierAppStatus {
			resp, err := http.Get(srv.URL + "/api/v1alpha1/three-tier-apps/pending-stack")
			if err != nil || resp.StatusCode != http.StatusOK {
				if resp != nil {
					_ = resp.Body.Close()
				}
				return v1alpha1.PENDING
			}
			defer resp.Body.Close()
			var stack v1alpha1.ThreeTierApp
			if err := json.NewDecoder(resp.Body).Decode(&stack); err != nil || stack.Status == nil {
				return v1alpha1.PENDING
			}
			return *stack.Status
		}, "15s", "25ms").Should(Equal(v1alpha1.RUNNING))

		client.setStatus("pending-stack", v1alpha1.PENDING)

		resp, err := http.Get(srv.URL + "/api/v1alpha1/three-tier-apps/pending-stack")
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		var stack v1alpha1.ThreeTierApp
		Expect(json.NewDecoder(resp.Body).Decode(&stack)).To(Succeed())
		Expect(stack.Status).To(HaveValue(Equal(v1alpha1.PENDING)))
	})
})

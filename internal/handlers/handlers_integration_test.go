package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/api/server"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/containerclient"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/handlers"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/store"
	"github.com/go-chi/chi/v5"
)


// statusOverrideClient wraps MockClient and overrides GetStatus for testing status changes.
type statusOverrideClient struct {
	inner    *containerclient.MockClient
	override map[string]v1alpha1.StackStatus
	mu       sync.RWMutex
}

func (c *statusOverrideClient) CreateContainers(ctx context.Context, stackID string, spec v1alpha1.ThreeTierSpec) (string, string, string, error) {
	return c.inner.CreateContainers(ctx, stackID, spec)
}

func (c *statusOverrideClient) DeleteContainers(ctx context.Context, stackID string) error {
	return c.inner.DeleteContainers(ctx, stackID)
}

func (c *statusOverrideClient) GetStatus(ctx context.Context, stackID string) (v1alpha1.StackStatus, bool) {
	c.mu.RLock()
	s, ok := c.override[stackID]
	c.mu.RUnlock()
	if ok {
		return s, true
	}
	return c.inner.GetStatus(ctx, stackID)
}

func (c *statusOverrideClient) setStatus(stackID string, status v1alpha1.StackStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.override == nil {
		c.override = make(map[string]v1alpha1.StackStatus)
	}
	c.override[stackID] = status
}

// mockStatusReporter records Publish and PublishDeleted calls for assertions.
type mockStatusReporter struct {
	mu       sync.Mutex
	publish  []publishCall
	deleted  []string
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

func TestHandlers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Handlers Suite")
}

var _ = Describe("Handlers with MockClient and status reporting", func() {
	var (
		srv     *httptest.Server
		reporter *mockStatusReporter
	)

	BeforeEach(func() {
		reporter = &mockStatusReporter{}
		h := &handlers.Handlers{
			Store:     store.NewMemoryStore(),
			Container: &containerclient.MockClient{},
			Status:    reporter,
		}
		r := chi.NewRouter()
		_ = server.HandlerFromMuxWithBaseURL(h, r, "/api/v1alpha1")
		srv = httptest.NewServer(r)
	})

	AfterEach(func() {
		if srv != nil {
			srv.Close()
		}
	})

	It("accepts database engine+version (Pet Clinic catalog), SP derives OCI image", func() {
		req := v1alpha1.CreateStackRequest{
			Metadata: v1alpha1.StackMetadata{Name: "db-engine-version-stack"},
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

		var stack v1alpha1.Stack
		Expect(json.NewDecoder(resp.Body).Decode(&stack)).To(Succeed())
		Expect(stack.Spec.Database.Engine).To(Equal("mysql"))
		Expect(stack.Spec.Database.Version).To(Equal("8"))
	})

	It("publishes RUNNING on create when status reporter is set", func() {
		req := v1alpha1.CreateStackRequest{
			Metadata: v1alpha1.StackMetadata{Name: "test-stack"},
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

		calls := reporter.getPublishCalls()
		Expect(calls).To(HaveLen(1))
		Expect(calls[0].InstanceID).To(Equal("test-stack"))
		Expect(calls[0].Status).To(Equal("RUNNING"))
	})

	It("returns status from MockClient on GET and publishes to reporter", func() {
		req := v1alpha1.CreateStackRequest{
			Metadata: v1alpha1.StackMetadata{Name: "get-status-stack"},
			Spec: v1alpha1.ThreeTierSpec{
				Database: v1alpha1.DatabaseTierSpec{Engine: "postgres", Version: "16"},
				App:      v1alpha1.AppTierSpec{Image: "app:latest"},
				Web:      v1alpha1.WebTierSpec{Image: "nginx:alpine"},
			},
		}
		body, _ := json.Marshal(req)
		_, err := http.Post(srv.URL+"/api/v1alpha1/three-tier-apps", "application/json", bytes.NewReader(body))
		Expect(err).NotTo(HaveOccurred())

		resp, err := http.Get(srv.URL + "/api/v1alpha1/three-tier-apps/get-status-stack")
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		var stack v1alpha1.Stack
		Expect(json.NewDecoder(resp.Body).Decode(&stack)).To(Succeed())
		Expect(stack.Status).To(Equal(v1alpha1.RUNNING))

		calls := reporter.getPublishCalls()
		Expect(calls).To(ContainElement(WithTransform(func(c publishCall) string { return c.InstanceID }, Equal("get-status-stack"))))
		Expect(calls).To(ContainElement(WithTransform(func(c publishCall) string { return c.Status }, Equal("RUNNING"))))
	})

	It("publishes DELETED on delete when status reporter is set", func() {
		req := v1alpha1.CreateStackRequest{
			Metadata: v1alpha1.StackMetadata{Name: "del-stack"},
			Spec: v1alpha1.ThreeTierSpec{
				Database: v1alpha1.DatabaseTierSpec{Engine: "postgres", Version: "16"},
				App:      v1alpha1.AppTierSpec{Image: "app:latest"},
				Web:      v1alpha1.WebTierSpec{Image: "nginx:alpine"},
			},
		}
		body, _ := json.Marshal(req)
		_, err := http.Post(srv.URL+"/api/v1alpha1/three-tier-apps", "application/json", bytes.NewReader(body))
		Expect(err).NotTo(HaveOccurred())

		delReq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1alpha1/three-tier-apps/del-stack", nil)
		resp, err := http.DefaultClient.Do(delReq)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusNoContent))

		deleted := reporter.getDeletedCalls()
		Expect(deleted).To(Equal([]string{"del-stack"}))
	})

	It("returns status from MockClient on List", func() {
		req := v1alpha1.CreateStackRequest{
			Metadata: v1alpha1.StackMetadata{Name: "list-stack"},
			Spec: v1alpha1.ThreeTierSpec{
				Database: v1alpha1.DatabaseTierSpec{Engine: "postgres", Version: "16"},
				App:      v1alpha1.AppTierSpec{Image: "app:latest"},
				Web:      v1alpha1.WebTierSpec{Image: "nginx:alpine"},
			},
		}
		body, _ := json.Marshal(req)
		_, err := http.Post(srv.URL+"/api/v1alpha1/three-tier-apps", "application/json", bytes.NewReader(body))
		Expect(err).NotTo(HaveOccurred())

		resp, err := http.Get(srv.URL + "/api/v1alpha1/three-tier-apps")
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		var list v1alpha1.StackList
		Expect(json.NewDecoder(resp.Body).Decode(&list)).To(Succeed())
		Expect(list.Stacks).NotTo(BeNil())
		Expect(*list.Stacks).To(HaveLen(1))
		Expect((*list.Stacks)[0].Status).To(Equal(v1alpha1.RUNNING))
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
		h := &handlers.Handlers{
			Store:     store.NewMemoryStore(),
			Container: client,
			Status:    reporter,
		}
		r := chi.NewRouter()
		_ = server.HandlerFromMuxWithBaseURL(h, r, "/api/v1alpha1")
		srv = httptest.NewServer(r)
	})

	AfterEach(func() {
		if srv != nil {
			srv.Close()
		}
	})

	It("returns FAILED and publishes FAILED when container status is FAILED", func() {
		req := v1alpha1.CreateStackRequest{
			Metadata: v1alpha1.StackMetadata{Name: "fail-stack"},
			Spec: v1alpha1.ThreeTierSpec{
				Database: v1alpha1.DatabaseTierSpec{Engine: "postgres", Version: "16"},
				App:      v1alpha1.AppTierSpec{Image: "app:latest"},
				Web:      v1alpha1.WebTierSpec{Image: "nginx:alpine"},
			},
		}
		body, _ := json.Marshal(req)
		_, err := http.Post(srv.URL+"/api/v1alpha1/three-tier-apps", "application/json", bytes.NewReader(body))
		Expect(err).NotTo(HaveOccurred())

		client.setStatus("fail-stack", v1alpha1.FAILED)

		resp, err := http.Get(srv.URL + "/api/v1alpha1/three-tier-apps/fail-stack")
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		var stack v1alpha1.Stack
		Expect(json.NewDecoder(resp.Body).Decode(&stack)).To(Succeed())
		Expect(stack.Status).To(Equal(v1alpha1.FAILED))

		calls := reporter.getPublishCalls()
		Expect(calls).To(ContainElement(WithTransform(func(c publishCall) string { return c.Status }, Equal("FAILED"))))
	})

	It("returns PENDING and publishes PENDING when container status is PENDING", func() {
		req := v1alpha1.CreateStackRequest{
			Metadata: v1alpha1.StackMetadata{Name: "pending-stack"},
			Spec: v1alpha1.ThreeTierSpec{
				Database: v1alpha1.DatabaseTierSpec{Engine: "postgres", Version: "16"},
				App:      v1alpha1.AppTierSpec{Image: "app:latest"},
				Web:      v1alpha1.WebTierSpec{Image: "nginx:alpine"},
			},
		}
		body, _ := json.Marshal(req)
		_, err := http.Post(srv.URL+"/api/v1alpha1/three-tier-apps", "application/json", bytes.NewReader(body))
		Expect(err).NotTo(HaveOccurred())

		client.setStatus("pending-stack", v1alpha1.PENDING)

		resp, err := http.Get(srv.URL + "/api/v1alpha1/three-tier-apps/pending-stack")
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		var stack v1alpha1.Stack
		Expect(json.NewDecoder(resp.Body).Decode(&stack)).To(Succeed())
		Expect(stack.Status).To(Equal(v1alpha1.PENDING))

		calls := reporter.getPublishCalls()
		Expect(calls).To(ContainElement(WithTransform(func(c publishCall) string { return c.Status }, Equal("PENDING"))))
	})
})

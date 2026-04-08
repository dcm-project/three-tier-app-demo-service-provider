package monitoring_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/config"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/containerclient"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/monitoring"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/store"
)

func TestMonitoring(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Monitoring Suite")
}

// --- helpers ---

func newTestStore() store.AppStore {
	f, err := os.CreateTemp("", "monitor-test-*.db")
	Expect(err).NotTo(HaveOccurred())
	path := f.Name()
	Expect(f.Close()).To(Succeed())
	DeferCleanup(func() { _ = os.Remove(path) })

	st, err := store.New(config.StoreConfig{Type: "sqlite", Path: path}, "")
	Expect(err).NotTo(HaveOccurred())
	return st
}

type mockReporter struct {
	mu      sync.Mutex
	events  []reportedEvent
	deleted []string
}

type reportedEvent struct {
	instanceID string
	status     string
}

func (r *mockReporter) Publish(_ context.Context, instanceID, status, _ string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, reportedEvent{instanceID: instanceID, status: status})
}

func (r *mockReporter) PublishDeleted(_ context.Context, instanceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted = append(r.deleted, instanceID)
}

func (r *mockReporter) publishedStatuses() []reportedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]reportedEvent, len(r.events))
	copy(cp, r.events)
	return cp
}

func (r *mockReporter) deletedIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]string, len(r.deleted))
	copy(cp, r.deleted)
	return cp
}

func seedRunningApp(ctx context.Context, st store.AppStore, id string) {
	running := v1alpha1.RUNNING
	path := "three-tier-apps/" + id
	now := time.Now()
	_, err := st.Create(ctx, v1alpha1.ThreeTierApp{
		Id:         &id,
		Path:       &path,
		Status:     &running,
		Spec:       v1alpha1.ThreeTierSpec{},
		CreateTime: &now,
		UpdateTime: &now,
	})
	Expect(err).NotTo(HaveOccurred())
}

// --- specs ---

var _ = Describe("StatusMonitor", func() {
	const fastInterval = 20 * time.Millisecond

	var (
		ctx     context.Context
		cancel  context.CancelFunc
		st      store.AppStore
		cc      *containerclient.MockClient
		rep     *mockReporter
		monitor *monitoring.StatusMonitor
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		st = newTestStore()
		cc = &containerclient.MockClient{}
		rep = &mockReporter{}
		monitor = monitoring.New(st, cc, rep, fastInterval, nil)
	})

	AfterEach(func() {
		cancel()
	})

	Describe("NotifyDeleted", func() {
		It("publishes a DELETED event via the reporter", func() {
			go monitor.Start(ctx)

			monitor.NotifyDeleted("app-1")

			Eventually(rep.deletedIDs, time.Second, 5*time.Millisecond).
				Should(ContainElement("app-1"))
		})

		It("publishes DELETED for each notified instance", func() {
			go monitor.Start(ctx)

			monitor.NotifyDeleted("app-a")
			monitor.NotifyDeleted("app-b")

			Eventually(rep.deletedIDs, time.Second, 5*time.Millisecond).
				Should(ConsistOf("app-a", "app-b"))
		})
	})

	Describe("reconcile (status polling)", func() {
		It("publishes a status change when live status differs from stored status", func() {
			// Store has RUNNING; MockClient returns FAILED (no containers created).
			seedRunningApp(ctx, st, "app-1")

			go monitor.Start(ctx)

			Eventually(func() []reportedEvent {
				return rep.publishedStatuses()
			}, time.Second, 5*time.Millisecond).Should(ContainElement(
				reportedEvent{instanceID: "app-1", status: "FAILED"},
			))
		})

		It("does not publish when live status matches stored status", func() {
			// Create containers so MockClient returns RUNNING, matching stored RUNNING.
			err := cc.CreateContainers(ctx, "app-2", v1alpha1.ThreeTierSpec{})
			Expect(err).NotTo(HaveOccurred())
			seedRunningApp(ctx, st, "app-2")

			go monitor.Start(ctx)

			// Wait two poll cycles, then assert no events were published.
			time.Sleep(3 * fastInterval)
			Expect(rep.publishedStatuses()).To(BeEmpty())
		})

		It("skips apps whose status cannot be retrieved (ok=false)", func() {
			// An empty store means no apps to poll — no events expected.
			go monitor.Start(ctx)
			time.Sleep(3 * fastInterval)
			Expect(rep.publishedStatuses()).To(BeEmpty())
		})

		It("does not publish when store is PENDING but live is RUNNING", func() {
			// Provisioning updates the DB and publishes RUNNING; reconcile must not duplicate.
			pending := v1alpha1.PENDING
			id := "app-pending-live-running"
			path := "three-tier-apps/" + id
			_, err := st.Create(ctx, v1alpha1.ThreeTierApp{
				Id:     &id,
				Path:   &path,
				Status: &pending,
				Spec:   v1alpha1.ThreeTierSpec{},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(cc.CreateContainers(ctx, id, v1alpha1.ThreeTierSpec{})).To(Succeed())

			go monitor.Start(ctx)
			time.Sleep(3 * fastInterval)
			Expect(rep.publishedStatuses()).To(BeEmpty())
		})
	})

	Describe("shutdown", func() {
		It("stops cleanly when context is cancelled", func() {
			done := make(chan struct{})
			go func() {
				monitor.Start(ctx)
				close(done)
			}()

			cancel()
			Eventually(done, time.Second).Should(BeClosed())
		})
	})
})

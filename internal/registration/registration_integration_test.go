package registration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	agentv1alpha1 "github.com/dcm-project/environment-agent/api/v1alpha1"

	"github.com/dcm-project/3-tier-demo-service-provider/internal/config"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/registration"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func testCfg(registrationURL string) *config.Config {
	return &config.Config{
		Provider: config.ProviderConfig{
			Name:        "3tier-sp",
			DisplayName: "Three Tier Demo SP",
			Endpoint:    "https://sp.example.com",
			Region:      "us-east-1",
			Zone:        "us-east-1a",
		},
		DCM: config.DCMConfig{
			RegistrationURL: registrationURL,
		},
	}
}

func agentOKResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(agentv1alpha1.Provider{
		Name:          "3tier-sp",
		ServiceType:   "three-tier-app-demo",
		Endpoint:      "https://sp.example.com/api/v1alpha1/three-tier-apps",
		SchemaVersion: "v1alpha1",
	})
}

var _ = Describe("Registration Integration", func() {

	var (
		mockServer *httptest.Server
		logBuf     *syncBuffer
		logger     *slog.Logger
	)

	BeforeEach(func() {
		logBuf = &syncBuffer{}
		logger = slog.New(slog.NewJSONHandler(logBuf, nil))
	})

	AfterEach(func() {
		if mockServer != nil {
			mockServer.Close()
		}
	})

	It("sends POST to {registrationUrl}/providers on startup", func() {
		var requestReceived atomic.Bool

		mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/api/v1alpha1/providers" {
				requestReceived.Store(true)
				agentOKResponse(w)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))

		registrar, err := registration.NewRegistrar(testCfg(mockServer.URL+"/api/v1alpha1"), logger)
		Expect(err).NotTo(HaveOccurred())
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		registrar.Start(ctx)

		Eventually(func() bool {
			return requestReceived.Load()
		}).WithTimeout(3 * time.Second).WithPolling(100 * time.Millisecond).Should(BeTrue(),
			"expected POST to /api/v1alpha1/providers but no request was received")
	})

	It("sends provider payload with collection endpoint and service type", func() {
		var receivedPayload agentv1alpha1.Provider
		var requestReceived atomic.Bool

		mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/api/v1alpha1/providers" {
				defer r.Body.Close()
				body, err := io.ReadAll(r.Body)
				if err == nil {
					_ = json.Unmarshal(body, &receivedPayload)
					requestReceived.Store(true)
				}
				agentOKResponse(w)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))

		registrar, err := registration.NewRegistrar(testCfg(mockServer.URL+"/api/v1alpha1"), logger)
		Expect(err).NotTo(HaveOccurred())
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		registrar.Start(ctx)

		Eventually(func() bool {
			return requestReceived.Load()
		}).WithTimeout(3 * time.Second).WithPolling(100 * time.Millisecond).Should(BeTrue(),
			"expected registration request but none was received")

		Expect(receivedPayload.Name).To(Equal("3tier-sp"))
		Expect(receivedPayload.ServiceType).To(Equal("three-tier-app-demo"))
		Expect(receivedPayload.SchemaVersion).To(Equal("v1alpha1"))
		Expect(receivedPayload.Endpoint).To(Equal("https://sp.example.com/api/v1alpha1/three-tier-apps"))
		Expect(receivedPayload.DisplayName).NotTo(BeNil())
		Expect(*receivedPayload.DisplayName).To(Equal("Three Tier Demo SP"))
		Expect(receivedPayload.Metadata).NotTo(BeNil())
		Expect(receivedPayload.Metadata.RegionCode).NotTo(BeNil())
		Expect(*receivedPayload.Metadata.RegionCode).To(Equal("us-east-1"))
		Expect(receivedPayload.Metadata.Zone).NotTo(BeNil())
		Expect(*receivedPayload.Metadata.Zone).To(Equal("us-east-1a"))
	})

	It("retries with increasing intervals and succeeds on 4th attempt", func() {
		var requestCount atomic.Int32
		var requestTimes []time.Time
		var mu sync.Mutex

		mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/api/v1alpha1/providers" {
				count := requestCount.Add(1)
				mu.Lock()
				requestTimes = append(requestTimes, time.Now())
				mu.Unlock()

				if count < 4 {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				agentOKResponse(w)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))

		registrar, err := registration.NewRegistrar(
			testCfg(mockServer.URL+"/api/v1alpha1"),
			logger,
			registration.SetInitialBackoff(10*time.Millisecond),
			registration.SetMaxBackoff(200*time.Millisecond),
			registration.SetReRegistrationInterval(time.Hour),
		)
		Expect(err).NotTo(HaveOccurred())
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		registrar.Start(ctx)

		Eventually(func() int32 {
			return requestCount.Load()
		}).WithTimeout(5 * time.Second).WithPolling(50 * time.Millisecond).Should(BeNumerically(">=", int32(4)),
			"expected at least 4 registration attempts")

		mu.Lock()
		defer mu.Unlock()
		Expect(requestTimes).To(HaveLen(4))
		for i := 2; i < len(requestTimes); i++ {
			prev := requestTimes[i-1].Sub(requestTimes[i-2])
			curr := requestTimes[i].Sub(requestTimes[i-1])
			Expect(curr).To(BeNumerically(">=", prev),
				"interval between attempts should increase (attempt %d)", i+1)
		}
	})

	It("logs errors and keeps registrar running on failure", func() {
		mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))

		registrar, err := registration.NewRegistrar(
			testCfg(mockServer.URL+"/api/v1alpha1"),
			logger,
			registration.SetInitialBackoff(10*time.Millisecond),
			registration.SetMaxBackoff(50*time.Millisecond),
		)
		Expect(err).NotTo(HaveOccurred())
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		registrar.Start(ctx)

		Eventually(func() string {
			return logBuf.String()
		}).WithTimeout(3 * time.Second).WithPolling(100 * time.Millisecond).Should(
			And(
				ContainSubstring("registration"),
				ContainSubstring("\"level\":\"WARN\""),
			),
			"expected WARN-level log entries about registration failures")
	})

	It("retries 409 on the re-registration cadence instead of giving up", func() {
		var requestCount atomic.Int32

		mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/api/v1alpha1/providers" {
				requestCount.Add(1)
				w.WriteHeader(http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))

		registrar, err := registration.NewRegistrar(
			testCfg(mockServer.URL+"/api/v1alpha1"),
			logger,
			registration.SetReRegistrationInterval(20*time.Millisecond),
		)
		Expect(err).NotTo(HaveOccurred())
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		registrar.Start(ctx)

		Eventually(func() int32 {
			return requestCount.Load()
		}).WithTimeout(3 * time.Second).WithPolling(20 * time.Millisecond).Should(BeNumerically(">=", int32(2)),
			"expected repeated 409 retries")
	})

	It("Done() channel closes after context cancellation", func() {
		mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/api/v1alpha1/providers" {
				agentOKResponse(w)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))

		registrar, err := registration.NewRegistrar(
			testCfg(mockServer.URL+"/api/v1alpha1"),
			logger,
			registration.SetReRegistrationInterval(50*time.Millisecond),
		)
		Expect(err).NotTo(HaveOccurred())
		ctx, cancel := context.WithCancel(context.Background())

		registrar.Start(ctx)
		time.Sleep(150 * time.Millisecond)
		cancel()

		Eventually(registrar.Done()).WithTimeout(3 * time.Second).Should(BeClosed(),
			"Done() channel should close after context cancellation")
	})
})

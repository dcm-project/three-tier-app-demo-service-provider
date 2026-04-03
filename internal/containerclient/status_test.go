package containerclient_test

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/containerclient"
	k8sapi "github.com/dcm-project/k8s-container-service-provider/api/v1alpha1"
)

func TestStatus(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Status Suite")
}

var _ = Describe("WorstStatusFromPodmanStates", func() {
	It("returns RUNNING when all 3 containers are running", func() {
		s, ok := containerclient.WorstStatusFromPodmanStates([]string{"running", "running", "running"})
		Expect(ok).To(BeTrue())
		Expect(s).To(Equal(v1alpha1.RUNNING))
	})

	It("returns FAILED when any container is exited", func() {
		s, ok := containerclient.WorstStatusFromPodmanStates([]string{"running", "exited", "running"})
		Expect(ok).To(BeTrue())
		Expect(s).To(Equal(v1alpha1.FAILED))
	})

	It("returns FAILED when any container is dead", func() {
		s, ok := containerclient.WorstStatusFromPodmanStates([]string{"running", "dead", "running"})
		Expect(ok).To(BeTrue())
		Expect(s).To(Equal(v1alpha1.FAILED))
	})

	It("returns FAILED when any container is removing", func() {
		s, ok := containerclient.WorstStatusFromPodmanStates([]string{"running", "removing", "running"})
		Expect(ok).To(BeTrue())
		Expect(s).To(Equal(v1alpha1.FAILED))
	})

	It("returns PENDING when any container is created", func() {
		s, ok := containerclient.WorstStatusFromPodmanStates([]string{"running", "created", "running"})
		Expect(ok).To(BeTrue())
		Expect(s).To(Equal(v1alpha1.PENDING))
	})

	It("returns PENDING when any container is paused", func() {
		s, ok := containerclient.WorstStatusFromPodmanStates([]string{"running", "paused", "running"})
		Expect(ok).To(BeTrue())
		Expect(s).To(Equal(v1alpha1.PENDING))
	})

	It("returns PENDING when all are created", func() {
		s, ok := containerclient.WorstStatusFromPodmanStates([]string{"created", "created", "created"})
		Expect(ok).To(BeTrue())
		Expect(s).To(Equal(v1alpha1.PENDING))
	})

	It("returns FAILED when states slice is empty", func() {
		s, ok := containerclient.WorstStatusFromPodmanStates([]string{})
		Expect(ok).To(BeTrue())
		Expect(s).To(Equal(v1alpha1.FAILED))
	})

	It("returns FAILED when fewer than 3 states (inspect failed)", func() {
		s, ok := containerclient.WorstStatusFromPodmanStates([]string{"running", "running"})
		Expect(ok).To(BeTrue())
		Expect(s).To(Equal(v1alpha1.FAILED))
	})
})

var _ = Describe("AggregateK8sContainerStatuses", func() {
	It("returns RUNNING when all tiers are RUNNING", func() {
		s, ok := containerclient.AggregateK8sContainerStatuses([]k8sapi.ContainerStatus{
			k8sapi.RUNNING, k8sapi.RUNNING, k8sapi.RUNNING,
		})
		Expect(ok).To(BeTrue())
		Expect(s).To(Equal(v1alpha1.RUNNING))
	})

	It("returns PENDING when any tier is PENDING", func() {
		s, ok := containerclient.AggregateK8sContainerStatuses([]k8sapi.ContainerStatus{
			k8sapi.RUNNING, k8sapi.PENDING, k8sapi.RUNNING,
		})
		Expect(ok).To(BeTrue())
		Expect(s).To(Equal(v1alpha1.PENDING))
	})

	It("returns FAILED when any tier is FAILED", func() {
		s, ok := containerclient.AggregateK8sContainerStatuses([]k8sapi.ContainerStatus{
			k8sapi.RUNNING, k8sapi.FAILED, k8sapi.RUNNING,
		})
		Expect(ok).To(BeTrue())
		Expect(s).To(Equal(v1alpha1.FAILED))
	})
})

var _ = Describe("MockClient GetStatus consistency", func() {
	It("returns FAILED for non-existent stack", func() {
		m := &containerclient.MockClient{}
		s, ok := m.GetStatus(context.Background(), "nonexistent")
		Expect(ok).To(BeTrue())
		Expect(s).To(Equal(v1alpha1.FAILED))
	})

	It("returns RUNNING for created stack", func() {
		m := &containerclient.MockClient{}
		_, _, _, err := m.CreateContainers(context.Background(), "s1", v1alpha1.ThreeTierSpec{
			Database: v1alpha1.DatabaseTierSpec{Engine: "postgres", Version: "16"},
			App:      v1alpha1.AppTierSpec{Image: "app:latest"},
			Web:      v1alpha1.WebTierSpec{Image: "nginx:alpine"},
		})
		Expect(err).NotTo(HaveOccurred())
		s, ok := m.GetStatus(context.Background(), "s1")
		Expect(ok).To(BeTrue())
		Expect(s).To(Equal(v1alpha1.RUNNING))
	})

	It("returns FAILED for stack after delete", func() {
		m := &containerclient.MockClient{}
		_, _, _, err := m.CreateContainers(context.Background(), "s1", v1alpha1.ThreeTierSpec{
			Database: v1alpha1.DatabaseTierSpec{Engine: "postgres", Version: "16"},
			App:      v1alpha1.AppTierSpec{Image: "app:latest"},
			Web:      v1alpha1.WebTierSpec{Image: "nginx:alpine"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.DeleteContainers(context.Background(), "s1")).To(Succeed())
		s, ok := m.GetStatus(context.Background(), "s1")
		Expect(ok).To(BeTrue())
		Expect(s).To(Equal(v1alpha1.FAILED))
	})
})

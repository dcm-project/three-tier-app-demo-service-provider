package config_test

import (
	"github.com/dcm-project/3-tier-demo-service-provider/internal/config"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Config", func() {
	Describe("Validate", func() {
		DescribeTable("WebExposure",
			func(cfg config.Config, wantErr bool) {
				err := cfg.Validate()
				if wantErr {
					Expect(err).To(HaveOccurred())
				} else {
					Expect(err).NotTo(HaveOccurred())
				}
			},
			Entry("kubernetes", config.Config{WebExposure: config.WebExposureKubernetes}, false),
			Entry("openshift", config.Config{WebExposure: config.WebExposureOpenShift}, false),
			Entry("empty string", config.Config{WebExposure: ""}, true),
			Entry("invalid", config.Config{WebExposure: "bogus"}, true),
		)
	})
})

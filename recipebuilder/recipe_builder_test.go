package recipebuilder_test

import (
	. "github.com/cloudfoundry-incubator/nsync/recipebuilder"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/pivotal-golang/lager"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Recipe Builder", func() {
	var (
		builder       *RecipeBuilder
		err           error
		desiredAppReq cc_messages.DesireAppRequestFromCC
		desiredLRP    models.DesiredLRP
		circuses      map[string]string
	)

	BeforeEach(func() {
		logger := lager.NewLogger("fakelogger")

		circuses = map[string]string{
			"some-stack": "some-circus.tgz",
		}

		builder = New(circuses, "the/docker/circus/path.tgz", "http://file-server.com", logger)

		desiredAppReq = cc_messages.DesireAppRequestFromCC{
			ProcessGuid:       "the-app-guid-the-app-version",
			DropletUri:        "http://the-droplet.uri.com",
			Stack:             "some-stack",
			StartCommand:      "the-start-command with-arguments",
			ExecutionMetadata: "the-execution-metadata",
			Environment: cc_messages.Environment{
				{Name: "foo", Value: "bar"},
			},
			MemoryMB:        128,
			DiskMB:          512,
			FileDescriptors: 32,
			NumInstances:    23,
			Routes:          []string{"route1", "route2"},
			LogGuid:         "the-log-id",
		}
	})

	JustBeforeEach(func() {
		desiredLRP, err = builder.Build(desiredAppReq)
	})

	Describe("CPU weight calculation", func() {
		Context("when the memory limit is below the minimum value", func() {
			BeforeEach(func() {
				desiredAppReq.MemoryMB = MinCpuProxy - 9999
			})
			It("returns 1", func() {
				Ω(desiredLRP.CPUWeight).Should(Equal(uint(1)))
			})
		})

		Context("when the memory limit is above the maximum value", func() {
			BeforeEach(func() {
				desiredAppReq.MemoryMB = MaxCpuProxy + 9999
			})
			It("returns 100", func() {
				Ω(desiredLRP.CPUWeight).Should(Equal(uint(100)))
			})
		})

		Context("when the memory limit is in between the minimum and maximum value", func() {
			BeforeEach(func() {
				desiredAppReq.MemoryMB = (MinCpuProxy + MaxCpuProxy) / 2
			})
			It("returns 50", func() {
				Ω(desiredLRP.CPUWeight).Should(Equal(uint(50)))
			})
		})
	})

	Context("when everything is correct", func() {
		It("does not error", func() {
			Ω(err).ShouldNot(HaveOccurred())
		})

		It("builds a valid DesiredLRP", func() {
			Ω(desiredLRP.ProcessGuid).Should(Equal("the-app-guid-the-app-version"))
			Ω(desiredLRP.Instances).Should(Equal(23))
			Ω(desiredLRP.Routes).Should(Equal([]string{"route1", "route2"}))
			Ω(desiredLRP.Stack).Should(Equal("some-stack"))
			Ω(desiredLRP.MemoryMB).Should(Equal(128))
			Ω(desiredLRP.DiskMB).Should(Equal(512))
			Ω(desiredLRP.Ports).Should(Equal([]models.PortMapping{{ContainerPort: 8080}}))

			Ω(desiredLRP.Log).Should(Equal(models.LogConfig{
				Guid:       "the-log-id",
				SourceName: "App",
			}))

			Ω(desiredLRP.Setup).Should(Equal(&models.ExecutorAction{
				models.SerialAction{
					[]models.ExecutorAction{
						{
							models.DownloadAction{
								From: "http://file-server.com/v1/static/some-circus.tgz",
								To:   "/tmp/circus",
							},
						},
						{
							models.DownloadAction{
								From:     "http://the-droplet.uri.com",
								To:       ".",
								CacheKey: "droplets-the-app-guid-the-app-version",
							},
						},
					},
				},
			}))

			runAction, ok := desiredLRP.Action.Action.(models.RunAction)
			Ω(ok).Should(BeTrue())

			monitorAction, ok := desiredLRP.Monitor.Action.(models.RunAction)
			Ω(ok).Should(BeTrue())

			Ω(monitorAction).Should(Equal(models.RunAction{
				Path: "/tmp/circus/spy",
				Args: []string{"-addr=:8080"},
			}))

			Ω(runAction.Path).Should(Equal("/tmp/circus/soldier"))
			Ω(runAction.Args).Should(Equal([]string{
				"/app",
				"the-start-command with-arguments",
				"the-execution-metadata",
			}))

			numFiles := uint64(32)
			Ω(runAction.ResourceLimits).Should(Equal(models.ResourceLimits{
				Nofile: &numFiles,
			}))

			Ω(runAction.Env).Should(ContainElement(models.EnvironmentVariable{
				Name:  "foo",
				Value: "bar",
			}))

			Ω(runAction.Env).Should(ContainElement(models.EnvironmentVariable{
				Name:  "PORT",
				Value: "8080",
			}))

			Ω(runAction.Env).Should(ContainElement(models.EnvironmentVariable{
				Name:  "VCAP_APP_PORT",
				Value: "8080",
			}))

			Ω(runAction.Env).Should(ContainElement(models.EnvironmentVariable{
				Name:  "VCAP_APP_HOST",
				Value: "0.0.0.0",
			}))
		})
	})

	Context("when there is a docker image url instead of a droplet uri", func() {
		BeforeEach(func() {
			desiredAppReq.DockerImageUrl = "user/repo:tag"
			desiredAppReq.DropletUri = ""
		})

		It("does not error", func() {
			Ω(err).ShouldNot(HaveOccurred())
		})

		It("converts the docker image url into a root fs path", func() {
			Ω(desiredLRP.RootFSPath).Should(Equal("docker:///user/repo#tag"))
		})

		It("uses the docker circus", func() {
			Ω(desiredLRP.Setup.Action.(models.SerialAction).Actions[0].Action).Should(Equal(models.DownloadAction{
				From: "http://file-server.com/v1/static/the/docker/circus/path.tgz",
				To:   "/tmp/circus",
			}))
		})

		Context("and the docker image url has no tag", func() {
			BeforeEach(func() {
				desiredAppReq.DockerImageUrl = "user/repo"
			})

			It("does not specify a url fragment for the tag, assumes garden-linux sets a default", func() {
				Ω(desiredLRP.RootFSPath).Should(Equal("docker:///user/repo"))
			})
		})
	})

	Context("when there is a docker image url AND a droplet uri", func() {
		BeforeEach(func() {
			desiredAppReq.DockerImageUrl = "user/repo:tag"
			desiredAppReq.DropletUri = "http://the-droplet.uri.com"
		})

		It("should error", func() {
			Ω(err).Should(MatchError(ErrMultipleAppSources))
		})
	})

	Context("when there is NEITHER a docker image url NOR a droplet uri", func() {
		BeforeEach(func() {
			desiredAppReq.DockerImageUrl = ""
			desiredAppReq.DropletUri = ""
		})

		It("should error", func() {
			Ω(err).Should(MatchError(ErrAppSourceMissing))
		})
	})

	Context("when there is no file descriptor limit", func() {
		BeforeEach(func() {
			desiredAppReq.FileDescriptors = 0
		})

		It("does not error", func() {
			Ω(err).ShouldNot(HaveOccurred())
		})

		It("does not set any FD limit on the run action", func() {
			runAction, ok := desiredLRP.Action.Action.(models.RunAction)
			Ω(ok).Should(BeTrue())

			Ω(runAction.ResourceLimits).Should(Equal(models.ResourceLimits{
				Nofile: nil,
			}))
		})
	})

	Context("when requesting a stack with no associated health-check", func() {
		BeforeEach(func() {
			desiredAppReq.Stack = "some-other-stack"
		})

		It("should error", func() {
			Ω(err).Should(MatchError(ErrNoCircusDefined))
		})
	})
})

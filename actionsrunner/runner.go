package actionsrunner

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"dagger.io/dagger"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

const runnerVersion = "2.300.2"

type Config struct {
	Token            string
	Repo             string
	RunnerNamePrefix string
	Labels           []string
	Count            int
}

func Run(ctx context.Context, c *dagger.Client, cfg Config) error {
	if cfg.Count == 0 {
		return fmt.Errorf("invalid count %d", cfg.Count)
	}

	arch := "x64"
	if runtime.GOARCH == "arm64" {
		arch = "arm64"
	}
	runnerFilename := "actions-runner-linux-" + arch + "-" + runnerVersion + ".tar.gz"
	runnerURL := "https://github.com/actions/runner/releases/download/v" + runnerVersion + "/" + runnerFilename

	base := c.Container().
		// From("mcr.microsoft.com/dotnet/runtime-deps:6.0").
		From("ubuntu:focal").
		WithExec([]string{"apt-get", "update"}).
		WithExec([]string{"useradd", "runner", "--create-home"})

	runnerDir := base.
		WithExec([]string{"apt-get", "install", "-y", "lsb-release", "curl", "tar", "unzip", "zip", "apt-transport-https", "ca-certificates", "sudo", "gpg-agent", "software-properties-common", "build-essential", "zlib1g-dev", "zstd", "gettext", "libcurl4-openssl-dev", "inetutils-ping", "jq", "wget", "dirmngr", "openssh-client", "locales", "python3-pip", "python3-setuptools", "python3", "dumb-init", "nodejs", "rsync", "gosu", "gcc"}).
		WithMountedDirectory("/opt/runner", c.Directory()).
		WithWorkdir("/opt/runner").
		WithExec([]string{"chown", "runner:runner", "/opt/runner"}).
		WithUser("runner").
		WithExec([]string{"curl", "-OL", runnerURL}).
		WithExec([]string{"tar", "-zxf", runnerFilename}).
		WithExec([]string{"rm", runnerFilename}).
		Directory("/opt/runner")

	installed := base.
		WithMountedDirectory("/opt/runner", runnerDir).
		WithWorkdir("/opt/runner").
		WithExec([]string{"./bin/installdependencies.sh"}).
		WithUser("runner")

	eg, ctx := errgroup.WithContext(ctx)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for i := 0; i < cfg.Count; i++ {
		eg.Go(func() error {
			defer cancel()
			// get a random uuid
			id, err := uuid.NewRandom()
			if err != nil {
				return err
			}
			_, err = installed.
				WithExec([]string{"./config.sh",
					"--url", cfg.Repo,
					"--token", cfg.Token,
					"--ephemeral",
					"--labels", strings.Join(cfg.Labels, ","),
					"--name", cfg.RunnerNamePrefix + "-" + id.String(),
					"--unattended",
				}).
				WithExec([]string{"./run.sh"}, dagger.ContainerWithExecOpts{
					ExperimentalPrivilegedNesting: true,
				}).
				ExitCode(ctx)
			if err != nil {
				panic(err)
			}
			return nil
		})
	}

	return eg.Wait()
}

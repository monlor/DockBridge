package command

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"dockbridge/internal/app"
	"dockbridge/internal/config"
)

type recordingRunner struct {
	calls []app.Invocation
}

func (r *recordingRunner) Run(_ context.Context, inv app.Invocation) error {
	r.calls = append(r.calls, inv)
	return nil
}

func TestHelpAndVersionDoNotLoadRuntime(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{name: "help", args: []string{"dockerbridge", "--help"}, want: "USAGE"},
		{name: "version", args: []string{"dockerbridge", "--version"}, want: "1.2.3"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			loads := 0
			err := Run(context.Background(), tt.args, Options{
				Version: "1.2.3",
				Writer:  &out,
				LoadConfig: func(config.Options) (config.Config, error) {
					loads++
					return config.Config{}, nil
				},
				NewRuntime: func(config.Config) Runner {
					t.Fatal("runtime should not be constructed")
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if loads != 0 {
				t.Fatalf("config loads = %d, want 0", loads)
			}
			if !strings.Contains(out.String(), tt.want) {
				t.Fatalf("output %q does not contain %q", out.String(), tt.want)
			}
		})
	}
}

func TestDockerRunDispatchPreservesRawArgs(t *testing.T) {
	runner, err := runWithRecordingRunner([]string{"dockerbridge", "run", "--rm", "-v", "$PWD:/app", "-p", "3000:3000", "image"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"run", "--rm", "-v", "$PWD:/app", "-p", "3000:3000", "image"}
	assertSingleInvocation(t, runner, want)
}

func TestComposeDispatchPreservesRawArgs(t *testing.T) {
	runner, err := runWithRecordingRunner([]string{"dockerbridge", "compose", "-f", "compose.yml", "up", "--build"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"compose", "-f", "compose.yml", "up", "--build"}
	assertSingleInvocation(t, runner, want)
}

func TestAliasExpandedDockerShapesDispatchToRuntime(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "docker run alias",
			args: []string{"dockerbridge", "run", "--rm", "alpine"},
			want: []string{"run", "--rm", "alpine"},
		},
		{
			name: "docker compose alias",
			args: []string{"dockerbridge", "compose", "up"},
			want: []string{"compose", "up"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := runWithRecordingRunner(tt.args)
			if err != nil {
				t.Fatal(err)
			}
			assertSingleInvocation(t, runner, tt.want)
		})
	}
}

func TestLegacyCommandPackagesAreNotRequired(t *testing.T) {
	for _, path := range []string{"../../cmd/docker", "../../cmd/docker-compose"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s should not exist as a first-party command package, stat err=%v", path, err)
		}
	}
}

func runWithRecordingRunner(args []string) (*recordingRunner, error) {
	runner := &recordingRunner{}
	err := Run(context.Background(), args, Options{
		LoadConfig: func(config.Options) (config.Config, error) {
			return config.Config{
				RealDockerPath:      "/bin/docker",
				RemoteDockerHost:    "ssh://dev",
				RemoteWorkspaceRoot: "/srv/dockbridge",
			}, nil
		},
		NewRuntime: func(config.Config) Runner {
			return runner
		},
		Env: func() map[string]string {
			return map[string]string{"DOCKBRIDGE_BYPASS": "1"}
		},
		WorkingDir: func() (string, error) {
			return "/work/project", nil
		},
	})
	return runner, err
}

func assertSingleInvocation(t *testing.T, runner *recordingRunner, want []string) {
	t.Helper()
	if len(runner.calls) != 1 {
		t.Fatalf("runtime calls = %d, want 1", len(runner.calls))
	}
	got := runner.calls[0]
	if got.Entrypoint != "dockerbridge" {
		t.Fatalf("entrypoint = %q, want dockerbridge", got.Entrypoint)
	}
	if !reflect.DeepEqual(got.Args, want) {
		t.Fatalf("args = %#v, want %#v", got.Args, want)
	}
	if got.Cwd != "/work/project" {
		t.Fatalf("cwd = %q, want /work/project", got.Cwd)
	}
}

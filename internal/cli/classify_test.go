package cli

import "testing"

func TestClassifyDockerCommands(t *testing.T) {
	tests := []struct {
		name       string
		entrypoint string
		args       []string
		wantKind   Kind
		wantFiles  bool
		wantPorts  bool
	}{
		{"docker ps passthrough", "docker", []string{"ps"}, KindPassthrough, false, false},
		{"docker context passthrough", "docker", []string{"context", "show"}, KindPassthrough, false, false},
		{"docker start passthrough", "docker", []string{"start", "web"}, KindPassthrough, false, false},
		{"docker run translated", "docker", []string{"run", "-v", ".:/app", "-p", "3000:3000", "nginx"}, KindDockerRun, true, true},
		{"docker compose translated", "docker", []string{"compose", "up"}, KindCompose, true, true},
		{"unsupported plugin command", "docker", []string{"buildx", "bake"}, KindUnsupported, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.entrypoint, tt.args)
			if got.Kind != tt.wantKind {
				t.Fatalf("Kind = %s, want %s", got.Kind, tt.wantKind)
			}
			if got.NeedsFileProjection != tt.wantFiles {
				t.Fatalf("NeedsFileProjection = %v, want %v", got.NeedsFileProjection, tt.wantFiles)
			}
			if got.NeedsPortProjection != tt.wantPorts {
				t.Fatalf("NeedsPortProjection = %v, want %v", got.NeedsPortProjection, tt.wantPorts)
			}
		})
	}
}

func TestParseRunMounts(t *testing.T) {
	got, err := ParseRunMounts([]string{
		"run",
		"-v", ".:/app:ro",
		"--volume=/tmp/cache:/cache",
		"--mount", "type=bind,source=./src,target=/src,readonly",
		"-v", "named-volume:/data",
	}, "/Users/me/project")
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 4 {
		t.Fatalf("len(ParseRunMounts) = %d, want 4", len(got))
	}
	if got[0].Source != "/Users/me/project" || got[0].Target != "/app" || !got[0].ReadOnly || got[0].IsNamedVolume {
		t.Fatalf("unexpected first mount: %+v", got[0])
	}
	if got[2].Source != "/Users/me/project/src" || got[2].Target != "/src" || !got[2].ReadOnly {
		t.Fatalf("unexpected --mount parse: %+v", got[2])
	}
	if !got[3].IsNamedVolume {
		t.Fatalf("named volume should not be local projection: %+v", got[3])
	}
}

func TestRewriteRunArgs(t *testing.T) {
	args := []string{"run", "-v", ".:/app:ro", "-p", "3000:3000", "nginx"}
	rewritten, err := RewriteRunArgs(args, "/work/app", map[string]string{"/work/app": "/remote/ws/app"}, map[int]int{3000: 49152})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"run", "-v", "/remote/ws/app:/app:ro", "-p", "127.0.0.1:49152:3000", "nginx"}
	if !equalStrings(rewritten, want) {
		t.Fatalf("RewriteRunArgs = %#v, want %#v", rewritten, want)
	}
}

func TestRewriteRunArgsMountForms(t *testing.T) {
	args := []string{
		"run",
		"--mount", "type=bind,source=./src,target=/src,readonly",
		"--mount=type=bind,src=/work/app/cache,destination=/cache",
		"nginx",
	}
	rewritten, err := RewriteRunArgs(args, "/work/app", map[string]string{
		"/work/app/src":   "/remote/ws/src",
		"/work/app/cache": "/remote/ws/cache",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"run",
		"--mount", "type=bind,source=/remote/ws/src,target=/src,readonly",
		"--mount=type=bind,src=/remote/ws/cache,destination=/cache",
		"nginx",
	}
	if !equalStrings(rewritten, want) {
		t.Fatalf("RewriteRunArgs = %#v, want %#v", rewritten, want)
	}
}

func TestResolveTildePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	mounts, err := ParseRunMounts([]string{"run", "-v", "~/src:/src"}, "/work/app")
	if err != nil {
		t.Fatal(err)
	}
	if mounts[0].Source != home+"/src" {
		t.Fatalf("tilde mount source = %q, want %q", mounts[0].Source, home+"/src")
	}

	contextPath := BuildContext([]string{"build", "~/src"}, "/work/app")
	if contextPath != home+"/src" {
		t.Fatalf("tilde build context = %q, want %q", contextPath, home+"/src")
	}
}

func TestRewriteBuildArgs(t *testing.T) {
	rewritten, err := RewriteBuildArgs([]string{"build", "-f", "Dockerfile", "."}, "/work/app", map[string]string{"/work/app": "/remote/ws/app"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"build", "-f", "Dockerfile", "/remote/ws/app"}
	if !equalStrings(rewritten, want) {
		t.Fatalf("RewriteBuildArgs = %#v, want %#v", rewritten, want)
	}
}

func TestParseRunPublishesAndEqualForms(t *testing.T) {
	got, err := ParseRunPublishes([]string{"run", "-p", "3000:3000", "--publish=127.0.0.1:8080:80", "-p=9000:90"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"3000:3000", "127.0.0.1:8080:80", "9000:90"}
	if !equalStrings(got, want) {
		t.Fatalf("ParseRunPublishes = %#v, want %#v", got, want)
	}
}

func TestRewriteRunArgsEqualForms(t *testing.T) {
	args := []string{"run", "--volume=./src:/src", "--publish=8080:80", "nginx"}
	rewritten, err := RewriteRunArgs(args, "/work/app", map[string]string{"/work/app/src": "/remote/src"}, map[int]int{8080: 49153})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"run", "--volume=/remote/src:/src", "--publish=127.0.0.1:49153:80", "nginx"}
	if !equalStrings(rewritten, want) {
		t.Fatalf("RewriteRunArgs = %#v, want %#v", rewritten, want)
	}
}

func TestRunCIDFileHelpers(t *testing.T) {
	path, ok, err := RunCIDFile([]string{"run", "--rm", "--cidfile", "/tmp/container.cid", "nginx"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || path != "/tmp/container.cid" {
		t.Fatalf("RunCIDFile separate value = %q, %v", path, ok)
	}

	path, ok, err = RunCIDFile([]string{"run", "--cidfile=/tmp/container.cid", "-it", "nginx"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || path != "/tmp/container.cid" {
		t.Fatalf("RunCIDFile equal value = %q, %v", path, ok)
	}

	rewritten := InjectRunCIDFile([]string{"run", "-it", "--rm", "nginx"}, "/tmp/dockbridge.cid")
	want := []string{"run", "--cidfile", "/tmp/dockbridge.cid", "-it", "--rm", "nginx"}
	if !equalStrings(rewritten, want) {
		t.Fatalf("InjectRunCIDFile = %#v, want %#v", rewritten, want)
	}
}

func TestParseRunMountsErrors(t *testing.T) {
	if _, err := ParseRunMounts([]string{"run", "-v"}, "/work"); err == nil {
		t.Fatal("expected missing -v value error")
	}
	if _, err := ParseRunPublishes([]string{"run", "-p"}); err == nil {
		t.Fatal("expected missing -p value error")
	}
	if _, _, err := RunCIDFile([]string{"run", "--cidfile"}); err == nil {
		t.Fatal("expected missing --cidfile value error")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

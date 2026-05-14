package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverComposeFilesDefaultAndExplicitOrder(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "compose.yml"), "services: {}\n")
	write(t, filepath.Join(dir, "compose.override.yml"), "services: {}\n")

	files, projectDir, err := DiscoverFiles(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if projectDir != dir || len(files) != 2 || filepath.Base(files[0]) != "compose.yml" || filepath.Base(files[1]) != "compose.override.yml" {
		t.Fatalf("default discovery mismatch files=%v projectDir=%s", files, projectDir)
	}

	files, projectDir, err = DiscoverFiles(dir, []string{"-f", "base.yml", "--file=prod.yml", "up"})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0] != filepath.Join(dir, "base.yml") || files[1] != filepath.Join(dir, "prod.yml") || projectDir != dir {
		t.Fatalf("explicit discovery mismatch files=%v projectDir=%s", files, projectDir)
	}
}

func TestTranslateComposeRewritesPathsAndPortsWithoutTouchingSource(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "compose.yml")
	original := `services:
  web:
    build: .
    volumes:
      - .:/app
      - named-data:/data
    ports:
      - "8080:80"
volumes:
  named-data: {}
`
	write(t, source, original)

	result, err := Translate(TranslateRequest{
		Files:           []string{source},
		ProjectDir:      dir,
		RemoteRoot:      "/remote/ws",
		RemotePortStart: 49152,
		OutputPath:      filepath.Join(t.TempDir(), "generated.yml"),
	})
	if err != nil {
		t.Fatal(err)
	}

	rendered, err := os.ReadFile(result.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(rendered)
	projectRemote := "/remote/ws/" + filepath.Base(dir)
	for _, want := range []string{projectRemote, projectRemote + ":/app", "127.0.0.1:49152:80", "named-data:/data"} {
		if !strings.Contains(text, want) {
			t.Fatalf("translated compose missing %q:\n%s", want, text)
		}
	}
	after, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != original {
		t.Fatalf("source compose was modified")
	}
	if len(result.FileMappings) != 1 || len(result.PortMappings) != 1 {
		t.Fatalf("unexpected mappings: %+v", result)
	}
}

func TestTranslateComposeMergesFilesAndBuildMapContext(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	override := filepath.Join(dir, "override.yml")
	write(t, base, "services:\n  web:\n    image: nginx\n")
	write(t, override, "services:\n  web:\n    build:\n      context: ./src\n    ports:\n      - \"127.0.0.1:9090:90/udp\"\n")
	if err := os.Mkdir(filepath.Join(dir, "src"), 0o700); err != nil {
		t.Fatal(err)
	}

	result, err := Translate(TranslateRequest{
		Files:           []string{base, override},
		ProjectDir:      dir,
		RemoteRoot:      "/remote/ws",
		RemotePortStart: 50000,
		OutputPath:      filepath.Join(t.TempDir(), "generated.yml"),
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := os.ReadFile(result.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(rendered)
	if !strings.Contains(text, "/remote/ws/"+filepath.Base(dir)+"/src") || !strings.Contains(text, "127.0.0.1:50000:90/udp") || !strings.Contains(text, "image: nginx") {
		t.Fatalf("unexpected rendered compose:\n%s", text)
	}
	if result.PortMappings[9090] != 50000 {
		t.Fatalf("port mapping missing: %+v", result.PortMappings)
	}
}

func TestTranslateComposeLongSyntaxAndTildePaths(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "compose.yml")
	write(t, source, `services:
  web:
    build:
      context: ~/src
    volumes:
      - type: bind
        source: ~/data
        target: /data
      - type: volume
        source: named-data
        target: /named
    ports:
      - target: 80
        published: "8080"
        protocol: udp
volumes:
  named-data: {}
`)

	result, err := Translate(TranslateRequest{
		Files:           []string{source},
		ProjectDir:      dir,
		RemoteRoot:      "/remote/ws",
		RemotePortStart: 49152,
		OutputPath:      filepath.Join(t.TempDir(), "generated.yml"),
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := os.ReadFile(result.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(rendered)
	for _, want := range []string{"source: /remote/ws/", "context: /remote/ws/", "host_ip: 127.0.0.1", "published: \"49152\"", "target: 80", "protocol: udp", "source: named-data"} {
		if !strings.Contains(text, want) {
			t.Fatalf("translated compose missing %q:\n%s", want, text)
		}
	}
	if _, ok := result.FileMappings[filepath.Join(home, "src")]; !ok {
		t.Fatalf("missing tilde build mapping: %+v", result.FileMappings)
	}
	if _, ok := result.FileMappings[filepath.Join(home, "data")]; !ok {
		t.Fatalf("missing tilde volume mapping: %+v", result.FileMappings)
	}
	if result.PortMappings[8080] != 49152 {
		t.Fatalf("port mapping missing: %+v", result.PortMappings)
	}
}

func TestDiscoverFilesErrors(t *testing.T) {
	if _, _, err := DiscoverFiles(t.TempDir(), nil); err == nil {
		t.Fatal("expected missing compose file error")
	}
	if _, _, err := DiscoverFiles(t.TempDir(), []string{"-f"}); err == nil {
		t.Fatal("expected missing -f value error")
	}
}

func TestIntValueVariants(t *testing.T) {
	tests := []struct {
		input any
		want  int
		ok    bool
	}{
		{8080, 8080, true},
		{int64(8081), 8081, true},
		{float64(8082), 8082, true},
		{"8083", 8083, true},
		{float64(80.5), 0, false},
		{"not-a-port", 0, false},
		{nil, 0, false},
	}
	for _, tt := range tests {
		got, ok := intValue(tt.input)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("intValue(%#v) = %d, %v; want %d, %v", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

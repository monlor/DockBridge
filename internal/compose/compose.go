package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type TranslateRequest struct {
	Files           []string
	ProjectDir      string
	RemoteRoot      string
	RemotePortStart int
	OutputPath      string
}

type TranslateResult struct {
	OutputPath   string
	FileMappings map[string]string
	PortMappings map[int]int
	Diagnostics  []string
}

func DiscoverFiles(cwd string, args []string) ([]string, string, error) {
	var explicit []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-f" || arg == "--file":
			if i+1 >= len(args) {
				return nil, "", fmt.Errorf("%s requires a value", arg)
			}
			explicit = append(explicit, abs(cwd, args[i+1]))
			i++
		case strings.HasPrefix(arg, "--file="):
			explicit = append(explicit, abs(cwd, strings.TrimPrefix(arg, "--file=")))
		case strings.HasPrefix(arg, "-f="):
			explicit = append(explicit, abs(cwd, strings.TrimPrefix(arg, "-f=")))
		}
	}
	if len(explicit) > 0 {
		return explicit, filepath.Dir(explicit[0]), nil
	}
	var files []string
	for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		path := filepath.Join(cwd, name)
		if _, err := os.Stat(path); err == nil {
			files = append(files, path)
			if name == "compose.yaml" || name == "compose.yml" {
				for _, override := range []string{"compose.override.yaml", "compose.override.yml"} {
					overridePath := filepath.Join(cwd, override)
					if _, err := os.Stat(overridePath); err == nil {
						files = append(files, overridePath)
					}
				}
			}
			return files, cwd, nil
		}
	}
	return nil, "", fmt.Errorf("no Compose file found in %s", cwd)
}

func Translate(req TranslateRequest) (TranslateResult, error) {
	merged := map[string]any{}
	for _, file := range req.Files {
		loaded, err := readYAML(file)
		if err != nil {
			return TranslateResult{}, err
		}
		merged = merge(merged, loaded)
	}

	result := TranslateResult{
		OutputPath:   req.OutputPath,
		FileMappings: map[string]string{},
		PortMappings: map[int]int{},
	}
	remotePort := req.RemotePortStart
	if remotePort == 0 {
		remotePort = 49152
	}
	projectName := filepath.Base(req.ProjectDir)
	projectRemoteRoot := filepath.ToSlash(filepath.Join(req.RemoteRoot, projectName))

	services, _ := merged["services"].(map[string]any)
	for _, serviceValue := range services {
		service, ok := serviceValue.(map[string]any)
		if !ok {
			continue
		}
		if build, ok := service["build"]; ok {
			switch typed := build.(type) {
			case string:
				local := resolve(req.ProjectDir, typed)
				remote := remoteForLocal(projectRemoteRoot, req.ProjectDir, local)
				result.FileMappings[local] = remote
				service["build"] = remote
			case map[string]any:
				if contextValue, ok := typed["context"].(string); ok && isLocal(contextValue) {
					local := resolve(req.ProjectDir, contextValue)
					remote := remoteForLocal(projectRemoteRoot, req.ProjectDir, local)
					result.FileMappings[local] = remote
					typed["context"] = remote
				}
			}
		}
		if volumes, ok := service["volumes"].([]any); ok {
			for i, value := range volumes {
				switch typed := value.(type) {
				case string:
					parts := strings.Split(typed, ":")
					if len(parts) < 2 || !isLocal(parts[0]) {
						continue
					}
					local := resolve(req.ProjectDir, parts[0])
					remote := remoteForLocal(projectRemoteRoot, req.ProjectDir, local)
					result.FileMappings[local] = remote
					parts[0] = remote
					volumes[i] = strings.Join(parts, ":")
				case map[string]any:
					source, ok := typed["source"].(string)
					if !ok || !isLocal(source) {
						continue
					}
					local := resolve(req.ProjectDir, source)
					remote := remoteForLocal(projectRemoteRoot, req.ProjectDir, local)
					result.FileMappings[local] = remote
					typed["source"] = remote
				}
			}
		}
		if ports, ok := service["ports"].([]any); ok {
			for i, value := range ports {
				switch typed := value.(type) {
				case string:
					local, container, suffix, ok := splitPort(typed)
					if !ok || local == 0 {
						continue
					}
					result.PortMappings[local] = remotePort
					ports[i] = fmt.Sprintf("127.0.0.1:%d:%d%s", remotePort, container, suffix)
					remotePort++
				case map[string]any:
					local, ok := intValue(typed["published"])
					if !ok || local == 0 {
						continue
					}
					if _, ok := intValue(typed["target"]); !ok {
						continue
					}
					result.PortMappings[local] = remotePort
					typed["host_ip"] = "127.0.0.1"
					typed["published"] = strconv.Itoa(remotePort)
					remotePort++
				}
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(req.OutputPath), 0o700); err != nil {
		return TranslateResult{}, err
	}
	data, err := yaml.Marshal(merged)
	if err != nil {
		return TranslateResult{}, err
	}
	if err := os.WriteFile(req.OutputPath, data, 0o600); err != nil {
		return TranslateResult{}, err
	}
	for local, remote := range result.FileMappings {
		result.Diagnostics = append(result.Diagnostics, "path "+local+" -> "+remote)
	}
	for local, remote := range result.PortMappings {
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("port 127.0.0.1:%d -> remote:%d", local, remote))
	}
	return result, nil
}

func readYAML(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func merge(base, override map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range base {
		out[key] = value
	}
	for key, value := range override {
		if baseMap, ok := out[key].(map[string]any); ok {
			if overrideMap, ok := value.(map[string]any); ok {
				out[key] = merge(baseMap, overrideMap)
				continue
			}
		}
		out[key] = value
	}
	return out
}

func abs(cwd, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(cwd, path)
}

func resolve(projectDir, source string) string {
	source = expandHomePath(source)
	if filepath.IsAbs(source) {
		return filepath.Clean(source)
	}
	return filepath.Clean(filepath.Join(projectDir, source))
}

func expandHomePath(source string) string {
	if source != "~" && !strings.HasPrefix(source, "~/") {
		return source
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return source
	}
	if source == "~" {
		return home
	}
	return filepath.Join(home, strings.TrimPrefix(source, "~/"))
}

func remoteForLocal(remoteProjectRoot, projectDir, local string) string {
	rel, err := filepath.Rel(projectDir, local)
	if err != nil || rel == "." {
		return filepath.ToSlash(remoteProjectRoot)
	}
	clean := filepath.Clean(rel)
	parts := strings.Split(clean, string(filepath.Separator))
	safe := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "." || part == ".." || part == "" {
			continue
		}
		safe = append(safe, part)
	}
	if len(safe) == 0 {
		return filepath.ToSlash(remoteProjectRoot)
	}
	return filepath.ToSlash(filepath.Join(append([]string{remoteProjectRoot}, safe...)...))
}

func isLocal(source string) bool {
	return source == "." || source == ".." || strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") || strings.HasPrefix(source, "/") || strings.HasPrefix(source, "~")
}

func splitPort(spec string) (int, int, string, bool) {
	value, protocol, _ := strings.Cut(spec, "/")
	suffix := ""
	if protocol != "" {
		suffix = "/" + protocol
	}
	parts := strings.Split(value, ":")
	switch len(parts) {
	case 2:
		local, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, 0, "", false
		}
		container, err := strconv.Atoi(parts[1])
		return local, container, suffix, err == nil
	case 3:
		local, err := strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, "", false
		}
		container, err := strconv.Atoi(parts[2])
		return local, container, suffix, err == nil
	default:
		return 0, 0, "", false
	}
}

func intValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	case string:
		parsed, err := strconv.Atoi(typed)
		return parsed, err == nil
	default:
		return 0, false
	}
}

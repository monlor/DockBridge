package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Kind string

const (
	KindPassthrough Kind = "passthrough"
	KindDockerRun   Kind = "docker-run"
	KindDockerBuild Kind = "docker-build"
	KindCompose     Kind = "compose"
	KindUnsupported Kind = "unsupported"
)

type Classification struct {
	Kind                Kind
	Command             string
	NeedsFileProjection bool
	NeedsPortProjection bool
	UnsupportedReason   string
}

func Classify(entrypoint string, args []string) Classification {
	if len(args) == 0 {
		return Classification{Kind: KindPassthrough, Command: ""}
	}
	command := args[0]
	switch command {
	case "compose":
		return Classification{Kind: KindCompose, Command: second(args), NeedsFileProjection: true, NeedsPortProjection: true}
	case "run":
		return Classification{Kind: KindDockerRun, Command: "run", NeedsFileProjection: hasMount(args), NeedsPortProjection: hasPublish(args)}
	case "build":
		return Classification{Kind: KindDockerBuild, Command: "build", NeedsFileProjection: true}
	case "ps", "logs", "exec", "images", "version", "info", "inspect", "network", "volume", "context", "start", "stop", "rm", "rmi", "pull", "push":
		return Classification{Kind: KindPassthrough, Command: command}
	default:
		return Classification{Kind: KindUnsupported, Command: command, UnsupportedReason: fmt.Sprintf("unsupported docker command %q; use DOCKBRIDGE_BYPASS=1 to call the real Docker CLI", command)}
	}
}

func first(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func second(args []string) string {
	if len(args) < 2 {
		return ""
	}
	return args[1]
}

func hasMount(args []string) bool {
	for _, arg := range args {
		if arg == "-v" || arg == "--volume" || strings.HasPrefix(arg, "-v=") || strings.HasPrefix(arg, "--volume=") || arg == "--mount" || strings.HasPrefix(arg, "--mount=") {
			return true
		}
	}
	return false
}

func hasPublish(args []string) bool {
	for _, arg := range args {
		if arg == "-p" || arg == "--publish" || strings.HasPrefix(arg, "-p=") || strings.HasPrefix(arg, "--publish=") {
			return true
		}
	}
	return false
}

type Mount struct {
	Source        string
	Target        string
	Type          string
	ReadOnly      bool
	IsNamedVolume bool
}

func ParseRunMounts(args []string, cwd string) ([]Mount, error) {
	var mounts []Mount
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-v" || arg == "--volume":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("%s requires a value", arg)
			}
			mounts = append(mounts, parseVolume(args[i+1], cwd))
			i++
		case strings.HasPrefix(arg, "--volume="):
			mounts = append(mounts, parseVolume(strings.TrimPrefix(arg, "--volume="), cwd))
		case strings.HasPrefix(arg, "-v="):
			mounts = append(mounts, parseVolume(strings.TrimPrefix(arg, "-v="), cwd))
		case arg == "--mount":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--mount requires a value")
			}
			mounts = append(mounts, parseMount(args[i+1], cwd))
			i++
		case strings.HasPrefix(arg, "--mount="):
			mounts = append(mounts, parseMount(strings.TrimPrefix(arg, "--mount="), cwd))
		}
	}
	return mounts, nil
}

func ParseRunPublishes(args []string) ([]string, error) {
	var publishes []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-p" || arg == "--publish":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("%s requires a value", arg)
			}
			publishes = append(publishes, args[i+1])
			i++
		case strings.HasPrefix(arg, "--publish="):
			publishes = append(publishes, strings.TrimPrefix(arg, "--publish="))
		case strings.HasPrefix(arg, "-p="):
			publishes = append(publishes, strings.TrimPrefix(arg, "-p="))
		}
	}
	return publishes, nil
}

func RunCIDFile(args []string) (string, bool, error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--cidfile":
			if i+1 >= len(args) {
				return "", false, fmt.Errorf("--cidfile requires a value")
			}
			return args[i+1], true, nil
		case strings.HasPrefix(arg, "--cidfile="):
			return strings.TrimPrefix(arg, "--cidfile="), true, nil
		}
	}
	return "", false, nil
}

func InjectRunCIDFile(args []string, cidfile string) []string {
	out := make([]string, 0, len(args)+2)
	if len(args) == 0 {
		return append(out, "--cidfile", cidfile)
	}
	out = append(out, args[0], "--cidfile", cidfile)
	out = append(out, args[1:]...)
	return out
}

func parseVolume(spec, cwd string) Mount {
	parts := strings.Split(spec, ":")
	if len(parts) == 1 {
		return Mount{Target: parts[0], Type: "volume", IsNamedVolume: true}
	}
	source := resolvePath(parts[0], cwd)
	readOnly := len(parts) > 2 && strings.Contains(parts[2], "ro")
	return Mount{
		Source:        source,
		Target:        parts[1],
		Type:          mountType(parts[0]),
		ReadOnly:      readOnly,
		IsNamedVolume: !isLocalPath(parts[0]),
	}
}

func parseMount(spec, cwd string) Mount {
	fields := map[string]string{}
	readOnly := false
	for _, part := range strings.Split(spec, ",") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			if part == "readonly" || part == "ro" {
				readOnly = true
			}
			continue
		}
		fields[key] = value
	}
	source := firstNonEmpty(fields["source"], fields["src"])
	target := firstNonEmpty(fields["target"], fields["dst"], fields["destination"])
	mountTypeValue := firstNonEmpty(fields["type"], mountType(source))
	return Mount{
		Source:        resolvePath(source, cwd),
		Target:        target,
		Type:          mountTypeValue,
		ReadOnly:      readOnly,
		IsNamedVolume: !isLocalPath(source),
	}
}

func RewriteRunArgs(args []string, cwd string, pathMappings map[string]string, remotePortMappings map[int]int) ([]string, error) {
	rewritten := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-v" || arg == "--volume":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("%s requires a value", arg)
			}
			rewritten = append(rewritten, arg, rewriteVolume(args[i+1], cwd, pathMappings))
			i++
		case strings.HasPrefix(arg, "--volume="):
			rewritten = append(rewritten, "--volume="+rewriteVolume(strings.TrimPrefix(arg, "--volume="), cwd, pathMappings))
		case arg == "--mount":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--mount requires a value")
			}
			rewritten = append(rewritten, arg, rewriteMount(args[i+1], cwd, pathMappings))
			i++
		case strings.HasPrefix(arg, "--mount="):
			rewritten = append(rewritten, "--mount="+rewriteMount(strings.TrimPrefix(arg, "--mount="), cwd, pathMappings))
		case arg == "-p" || arg == "--publish":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("%s requires a value", arg)
			}
			rewritten = append(rewritten, arg, rewritePublish(args[i+1], remotePortMappings))
			i++
		case strings.HasPrefix(arg, "--publish="):
			rewritten = append(rewritten, "--publish="+rewritePublish(strings.TrimPrefix(arg, "--publish="), remotePortMappings))
		default:
			rewritten = append(rewritten, arg)
		}
	}
	return rewritten, nil
}

func BuildContext(args []string, cwd string) string {
	contextArg := "."
	skipNext := false
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if skipNext {
			skipNext = false
			continue
		}
		if arg == "-f" || arg == "--file" || arg == "-t" || arg == "--tag" {
			skipNext = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		contextArg = arg
	}
	return resolvePath(contextArg, cwd)
}

func RewriteBuildArgs(args []string, cwd string, pathMappings map[string]string) ([]string, error) {
	contextPath := BuildContext(args, cwd)
	remote := pathMappings[contextPath]
	if remote == "" {
		return append([]string{}, args...), nil
	}
	rewritten := append([]string{}, args...)
	for i := len(rewritten) - 1; i >= 1; i-- {
		arg := rewritten[i]
		if strings.HasPrefix(arg, "-") {
			continue
		}
		rewritten[i] = remote
		return rewritten, nil
	}
	return append(rewritten, remote), nil
}

func rewriteVolume(spec, cwd string, pathMappings map[string]string) string {
	parts := strings.Split(spec, ":")
	if len(parts) < 2 || !isLocalPath(parts[0]) {
		return spec
	}
	local := resolvePath(parts[0], cwd)
	if remote, ok := pathMappings[local]; ok {
		parts[0] = remote
	}
	return strings.Join(parts, ":")
}

func rewriteMount(spec, cwd string, pathMappings map[string]string) string {
	parts := strings.Split(spec, ",")
	for i, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		if !ok || (key != "source" && key != "src") || !isLocalPath(value) {
			continue
		}
		local := resolvePath(value, cwd)
		if remote, ok := pathMappings[local]; ok {
			parts[i] = key + "=" + remote
		}
	}
	return strings.Join(parts, ",")
}

func rewritePublish(spec string, remotePortMappings map[int]int) string {
	value, protocol, _ := strings.Cut(spec, "/")
	parts := strings.Split(value, ":")
	switch len(parts) {
	case 2:
		host, err := strconv.Atoi(parts[0])
		if err == nil {
			if remote, ok := remotePortMappings[host]; ok {
				parts[0] = "127.0.0.1"
				parts = append(parts[:1], strconv.Itoa(remote), parts[1])
			}
		}
	case 3:
		host, err := strconv.Atoi(parts[1])
		if err == nil {
			if remote, ok := remotePortMappings[host]; ok {
				parts[0] = "127.0.0.1"
				parts[1] = strconv.Itoa(remote)
			}
		}
	}
	out := strings.Join(parts, ":")
	if protocol != "" {
		out += "/" + protocol
	}
	return out
}

func resolvePath(source, cwd string) string {
	if source == "" || !isLocalPath(source) {
		return source
	}
	source = expandHomePath(source)
	if filepath.IsAbs(source) {
		return filepath.Clean(source)
	}
	return filepath.Clean(filepath.Join(cwd, source))
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

func mountType(source string) string {
	if isLocalPath(source) {
		return "bind"
	}
	return "volume"
}

func isLocalPath(source string) bool {
	return source == "." || source == ".." || strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") || strings.HasPrefix(source, "/") || strings.HasPrefix(source, "~")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

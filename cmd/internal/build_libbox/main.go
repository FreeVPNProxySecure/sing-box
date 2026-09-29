package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	_ "github.com/sagernet/gomobile"
	"github.com/sagernet/sing-box/cmd/internal/build_shared"
	"github.com/sagernet/sing-box/cmd/internal/gomobile_rules"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/rw"
	"github.com/sagernet/sing/common/shell"
)

var (
	debugEnabled  bool
	target        string
	platform      string
	withTailscale bool
)

func init() {
	flag.BoolVar(&debugEnabled, "debug", false, "enable debug")
	flag.StringVar(&target, "target", "android", "target platform")
	flag.StringVar(&platform, "platform", "", "specify platform")
	flag.BoolVar(&withTailscale, "with-tailscale", false, "build tailscale for iOS and tvOS")
}

func main() {
	flag.Parse()

	build_shared.FindMobile()

	switch target {
	case "android":
		buildAndroid()
	case "apple":
		buildApple()
	default:
		log.Fatal("unknown target: ", target)
	}
}

var (
	sharedFlags []string
	debugFlags  []string
	sharedTags  []string
	darwinTags  []string
	memcTags    []string
	notMemcTags []string
	debugTags   []string
)

const extLd = `-extldflags "-Wl,-z,max-page-size=16384 -Wl,-z,common-page-size=16384"`

func init() {
	sharedFlags = append(sharedFlags, "-trimpath")
	sharedFlags = append(sharedFlags, "-buildvcs=false")
	currentTag, err := build_shared.ReadTag()
	if err != nil {
		currentTag = "unknown"
	}
	sharedLd := []string{
		"-X github.com/sagernet/sing-box/constant.Version=" + currentTag,
		"-s -w -buildid=",
		extLd,
	}
	sharedFlags = append(sharedFlags, "-ldflags", strings.Join(sharedLd, " "))

	debugLd := []string{
		"-X github.com/sagernet/sing-box/constant.Version=" + currentTag,
		extLd,
	}
	debugFlags = append(debugFlags, "-ldflags", strings.Join(debugLd, " "))

	sharedTags = append(sharedTags, "with_gvisor", "with_quic", "with_wireguard", "with_utls", "with_clash_api", "with_conntrack")
	darwinTags = append(darwinTags, "with_dhcp")
	memcTags = append(memcTags, "with_tailscale")
	notMemcTags = append(notMemcTags, "with_low_memory")
	debugTags = append(debugTags, "debug")
}

func buildAndroid() {
	build_shared.FindSDK()

	var javaPath string
	javaHome := os.Getenv("JAVA_HOME")
	if javaHome == "" {
		javaPath = "java"
	} else {
		javaPath = filepath.Join(javaHome, "bin", "java")
	}

	javaVersion, err := shell.Exec(javaPath, "--version").ReadOutput()
	if err != nil {
		log.Fatal(E.Cause(err, "check java version"))
	}
	if !strings.Contains(javaVersion, "openjdk 17") {
		log.Fatal("java version should be openjdk 17")
	}

	var bindTarget string
	if platform != "" {
		bindTarget = platform
	} else if debugEnabled {
		bindTarget = "android/arm64"
	} else {
		bindTarget = "android"
	}

	args := []string{
		"bind",
		"-v",
		"-target", bindTarget,
		"-androidapi", "21",
		"-javapkg=com.securedproxy",
		"-libname=box",
	}

	if !debugEnabled {
		args = append(args, addCheckLinkName(sharedFlags)...)
	} else {
		args = append(args, addCheckLinkName(debugFlags)...)
	}

	tags := append([]string{}, sharedTags...)
	tags = append(tags, memcTags...)
	if debugEnabled {
		tags = append(tags, debugTags...)
	}
	args = append(args, "-tags", strings.Join(tags, ","))
	args = append(args, "./experimental/libbox")

	cmd := exec.Command(build_shared.GoBinPath+"/gomobile", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	env := os.Environ()
	if os.Getenv("CGO_ENABLED") == "" {
		env = append(env, "CGO_ENABLED=1")
	}
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		log.Fatal(err)
	}

	const name = "libbox.aar"
	if err := gomobile_rules.ReplaceInAAR(name); err != nil {
		log.Fatal(E.Cause(err, "write consumer keep rules"))
	}
	copyPath := filepath.Join("..", "sing-box-for-android", "app", "libs")
	if rw.IsDir(copyPath) {
		copyPath, _ = filepath.Abs(copyPath)
		if err := rw.CopyFile(name, filepath.Join(copyPath, name)); err != nil {
			log.Fatal(err)
		}
		log.Info("copied to ", copyPath)
	}
}

func buildApple() {
	var bindTarget string
	if platform != "" {
		bindTarget = platform
	} else if debugEnabled {
		bindTarget = "ios"
	} else {
		bindTarget = "ios,tvos,macos"
	}

	args := []string{
		"bind",
		"-v",
		"-target", bindTarget,
		"-libname=box",
		"-tags-not-macos=" + strings.Join(notMemcTags, ","),
	}
	if !withTailscale {
		args = append(args, "-tags-macos="+strings.Join(memcTags, ","))
	}

	if !debugEnabled {
		args = append(args, sharedFlags...)
	} else {
		args = append(args, debugFlags...)
	}

	tags := append([]string{}, sharedTags...)
	tags = append(tags, darwinTags...)
	if withTailscale {
		tags = append(tags, memcTags...)
	}
	if debugEnabled {
		tags = append(tags, debugTags...)
	}

	args = append(args, "-tags", strings.Join(tags, ","))
	args = append(args, "./experimental/libbox")

	cmd := exec.Command(build_shared.GoBinPath+"/gomobile", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		log.Fatal(err)
	}

	copyPath := filepath.Join("..", "sing-box-for-apple")
	if rw.IsDir(copyPath) {
		targetDir := filepath.Join(copyPath, "Libbox.xcframework")
		targetDir, _ = filepath.Abs(targetDir)
		_ = os.RemoveAll(targetDir)
		_ = os.Rename("Libbox.xcframework", targetDir)
		log.Info("copied to ", targetDir)
	}
}

func addCheckLinkName(flags []string) []string {
	out := make([]string, len(flags))
	copy(out, flags)
	for i := 0; i < len(out)-1; i++ {
		if out[i] == "-ldflags" {
			out[i+1] = out[i+1] + " -checklinkname=0"
			break
		}
	}
	return out
}

package builder

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Cloud-Foundations/Dominator/lib/format"
	"github.com/Cloud-Foundations/Dominator/lib/goroutine"
	"github.com/Cloud-Foundations/Dominator/lib/image"
	"github.com/Cloud-Foundations/Dominator/lib/srpc"
	proto "github.com/Cloud-Foundations/Dominator/proto/imaginator"
)

const (
	cmdPerms = syscall.S_IRWXU | syscall.S_IRGRP | syscall.S_IXGRP |
		syscall.S_IROTH | syscall.S_IXOTH
	dirPerms = syscall.S_IRWXU | syscall.S_IRGRP | syscall.S_IXGRP |
		syscall.S_IROTH | syscall.S_IXOTH
	packagerPathname = "/bin/generic-packager"
)

var environmentToCopy = map[string]struct{}{
	"PATH":  {},
	"TZ":    {},
	"SHELL": {},
}

var environmentToSet = map[string]string{
	"HOME":    "/",
	"LOGNAME": "root",
	"USER":    "root",
}

func cleanPackages(g *goroutine.Goroutine, rootDir string,
	buildLog io.Writer) error {
	fmt.Fprintln(buildLog, "\nCleaning packages:")
	startTime := time.Now()
	err := runInTarget(g, nil, buildLog, rootDir, nil, packagerPathname,
		"clean")
	if err != nil {
		return errors.New("error cleaning: " + err.Error())
	}
	fmt.Fprintf(buildLog, "Package clean took: %s\n",
		format.Duration(time.Since(startTime)))
	return nil
}

func clearResolvConf(g *goroutine.Goroutine, writer io.Writer,
	rootDir string) error {
	return runInTarget(g, nil, writer, rootDir, nil,
		"/bin/cp", "/dev/null", "/etc/resolv.conf")
}

func makeTempDirectory(dir, prefix string) (string, error) {
	tmpDir, err := ioutil.TempDir(dir, prefix)
	if err != nil {
		return "", err
	}
	if err := os.Chmod(tmpDir, dirPerms); err != nil {
		os.RemoveAll(tmpDir)
		return "", err
	}
	return tmpDir, nil
}

func (stream *bootstrapStream) build(b *Builder, client srpc.ClientI,
	request proto.BuildImageRequest,
	buildLog buildLogger) (*image.Image, error) {
	startTime := time.Now()
	args := make([]string, 0, len(stream.BootstrapCommand))
	rootDir, err := makeTempDirectory("",
		strings.Replace(request.StreamName, "/", "_", -1))
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(rootDir)
	fmt.Fprintf(buildLog, "Created image working directory: %s\n", rootDir)
	for _, arg := range stream.BootstrapCommand {
		if arg == "$dir" {
			arg = rootDir
		}
		args = append(args, arg)
	}
	fmt.Fprintf(buildLog, "Running command: %s with args:\n", args[0])
	for _, arg := range args[1:] {
		fmt.Fprintf(buildLog, "    %s\n", arg)
	}
	g, err := newNamespaceTarget()
	if err != nil {
		return nil, err
	}
	defer g.Quit()
	err = runInTarget(g, nil, buildLog, "", nil, args[0], args[1:]...)
	if err != nil {
		return nil, err
	} else {
		packager := b.packagerTypes[stream.PackagerType]
		if err := packager.writePackageInstaller(rootDir); err != nil {
			return nil, err
		}
		if err := clearResolvConf(g, buildLog, rootDir); err != nil {
			return nil, err
		}
		buildDuration := time.Since(startTime)
		fmt.Fprintf(buildLog, "\nBuild time: %s\n",
			format.Duration(buildDuration))
		if err := cleanPackages(g, rootDir, buildLog); err != nil {
			return nil, err
		}
		return packImage(g, client, request, rootDir,
			stream.Filter, nil, nil, stream.imageFilter, stream.imageTriggers,
			buildLog)
	}
}

func (packager *packagerType) writePackageInstaller(rootDir string) error {
	filename := filepath.Join(rootDir, packagerPathname)
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY, cmdPerms)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	defer writer.Flush()
	packager.writePackageInstallerContents(writer)
	return writer.Flush()
}

func (packager *packagerType) writePackageInstallerContents(writer io.Writer) {
	fmt.Fprintln(writer, "#! /bin/sh")
	fmt.Fprintln(writer, "# Created by imaginator.")
	for _, line := range packager.Verbatim {
		fmt.Fprintln(writer, line)
	}
	fmt.Fprintln(writer, "cmd=\"$1\"; shift")
	writePackagerCommand(writer, "clean", packager.CleanCommand)
	fmt.Fprintln(writer, `[ "$cmd" = "copy-in" ] && exec cat > "$1"`)
	writePackagerCommand(writer, "install", packager.InstallCommand)
	writePackagerCommand(writer, "list", packager.ListCommand.ArgList)
	writePackagerCommand(writer, "remove", packager.RemoveCommand)
	fmt.Fprintln(writer, `[ "$cmd" = "run" ] && exec "$@"`)
	multiplier := packager.ListCommand.SizeMultiplier
	if multiplier < 1 {
		multiplier = 1
	}
	fmt.Fprintf(writer,
		"[ \"$cmd\" = \"show-size-multiplier\" ] && exec echo %d\n", multiplier)
	writePackagerCommand(writer, "update", packager.UpdateCommand)
	writePackagerCommand(writer, "upgrade", packager.UpgradeCommand)
	fmt.Fprintln(writer, "echo \"Invalid command: $cmd\"")
	fmt.Fprintln(writer, "exit 2")
}

func writePackagerCommand(writer io.Writer, cmd string, command []string) {
	if len(command) < 1 {
		fmt.Fprintf(writer, "[ \"$cmd\" = \"%s\" ] && exit 0\n", cmd)
	} else {
		fmt.Fprintf(writer, "[ \"$cmd\" = \"%s\" ] && exec", cmd)
		for _, arg := range command {
			writeArgument(writer, arg)
		}
		fmt.Fprintf(writer, " \"$@\"\n")
	}
}

func writeArgument(writer io.Writer, arg string) {
	if len(strings.Fields(arg)) < 2 {
		fmt.Fprintf(writer, " %s", arg)
	} else {
		lenArg := len(arg)
		if lenArg > 0 && arg[lenArg-1] == '\n' {
			arg = arg[:lenArg-1] + `\n`
		}
		fmt.Fprintf(writer, " '%s'", arg)
	}
}

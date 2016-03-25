package helpers

import (
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

var (
	dockerContainerName = "dockerdaemon"
)

func readRand(r *rand.Rand, p []byte) {
	for i := 0; i < len(p); i += 7 {
		val := r.Int63()
		for j := 0; i+j < len(p) && j < 7; j++ {
			p[i+j] = byte(val)
			val >>= 8
		}
	}
}

func randomFile(name string, blockSize, blocks int) error {
	rf, err := os.Create(name)
	if err != nil {
		return err
	}
	defer rf.Close()

	buf := make([]byte, blockSize)
	r := rand.New(rand.NewSource(time.Now().Unix()))
	for i := 0; i < blocks; i++ {
		readRand(r, buf)
		if _, err := rf.Write(buf); err != nil {
			return err
		}
	}

	return nil
}

func TempImage(name string) error {
	td, err := ioutil.TempDir("", "")
	if err != nil {
		return err
	}
	defer os.RemoveAll(td)

	if err := randomFile(filepath.Join(td, "f"), 1024, 512); err != nil {
		return err
	}

	tempDockerfile := []byte(`FROM scratch
COPY f /f

CMD []
`)
	if err := ioutil.WriteFile(filepath.Join(td, "Dockerfile"), tempDockerfile, 0666); err != nil {
		return err
	}

	if err := dockerCP(td, "/tmpbuild"); err != nil {
		return err
	}

	buildCommand := fmt.Sprintf("cd /tmpbuild/; docker build --no-cache -t %s .; rm -rf /tmpbuild/", name)
	if err := dockerExec(buildCommand); err != nil {
		return fmt.Errorf("build error: %v", err)
	}

	return nil
}

func DockerRun(args ...string) error {
	out, err := DockerRunWithOutput(args...)
	fmt.Print(out)
	return err
}

func DockerRunWithOutput(args ...string) (string, error) {
	cmdArgs := []string{
		"exec",
		dockerContainerName,
		"docker",
	}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.Command("docker", cmdArgs...)

	out, status, err := runCommandWithOutput(cmd)
	if err != nil {
		return out, err
	}
	if status != 0 {
		return out, fmt.Errorf("exit status %d running docker", status)
	}

	return out, nil
}

func getExitCode(err error) (int, error) {
	exitCode := 0
	if exiterr, ok := err.(*exec.ExitError); ok {
		if procExit, ok := exiterr.Sys().(syscall.WaitStatus); ok {
			return procExit.ExitStatus(), nil
		}
	}
	return exitCode, fmt.Errorf("failed to get exit code")
}

func processExitCode(err error) (exitCode int) {
	if err != nil {
		var exiterr error
		if exitCode, exiterr = getExitCode(err); exiterr != nil {
			// TODO: Fix this so we check the error's text.
			// we've failed to retrieve exit code, so we set it to 127
			exitCode = 127
		}
	}
	return
}

func runCommand(cmd *exec.Cmd) (exitCode int, err error) {
	exitCode = 0
	err = cmd.Run()
	exitCode = processExitCode(err)
	return
}

func runCommandWithOutput(cmd *exec.Cmd) (output string, exitCode int, err error) {
	exitCode = 0
	out, err := cmd.CombinedOutput()
	exitCode = processExitCode(err)
	output = string(out)
	return
}

func dockerCP(source, dest string) error {
	cmd := exec.Command("docker", "cp", source, fmt.Sprintf("%s:%s", dockerContainerName, dest))
	status, err := runCommand(cmd)
	if err != nil {
		return err
	}
	if status != 0 {
		return fmt.Errorf("exit status %d copying %s to %s", status, source, dest)
	}

	return nil
}

func dockerExec(command string) error {
	cmd := exec.Command("docker", "exec", dockerContainerName, "sh", "-c", command)
	out, status, err := runCommandWithOutput(cmd)
	fmt.Println(out)
	if err != nil {
		return fmt.Errorf("run error on %q: %v", command, err)
	}
	if status != 0 {
		return fmt.Errorf("exit status %d execing %q", status, command)
	}

	return nil
}

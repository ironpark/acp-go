// Command dual-client connects to an agent with ACP v2 when the agent
// supports it and falls back to v1 when it does not, then runs one prompt.
//
//	go run ./examples/dual-client           # dual-agent: speaks v2
//	go build -o /tmp/echo ./examples/echo
//	go run ./examples/dual-client /tmp/echo # v1 only: falls back
//
// router.ClientConnector spawns the agent and initializes with v2 first. An
// agent that answers protocolVersion 1 is restarted and initialized with v1,
// so each version sends its own initialize request. The session code then
// branches once on the version it got, into v1.go or v2.go.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/ironpark/acp-go/acp1"
	"github.com/ironpark/acp-go/acp2"
	"github.com/ironpark/acp-go/router"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, command []string) error {
	if len(command) == 0 {
		binary, cleanup, err := buildDualAgent()
		if err != nil {
			return err
		}
		defer cleanup()
		command = []string{binary}
	}

	clientInfo := &acp1.Implementation{Name: "dual-client", Version: "0.1.0"}
	v2 := &v2Client{}
	agent, err := router.NewClient().
		WithV1(func(*acp1.ClientSideConnection) acp1.Client { return v1Client{} },
			&acp1.InitializeRequest{ClientInfo: clientInfo}).
		WithV2(func(*acp2.ClientSideConnection) acp2.Client { return v2 },
			&acp2.InitializeRequest{Info: acp2.Implementation{Name: clientInfo.Name, Version: clientInfo.Version}}).
		// Spawn may start the agent twice, so it takes a command factory.
		Spawn(ctx, func() *exec.Cmd { return exec.Command(command[0], command[1:]...) })
	if err != nil {
		return err
	}
	defer agent.Close() // Close, Wait, Done and extension calls work on either version

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	const prompt = "hello from dual-client"
	if agent.V2 != nil {
		fmt.Printf("negotiated v2 with %s\n", agent.V2Init.Info.Name)
		return promptV2(ctx, agent.V2, v2, cwd, prompt)
	}
	fmt.Println("negotiated v1")
	return promptV1(ctx, agent.V1, cwd, prompt)
}

// buildDualAgent compiles the dual-agent example into a temporary directory.
func buildDualAgent() (binary string, cleanup func(), err error) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", nil, fmt.Errorf("cannot locate this source file")
	}
	dir, err := os.MkdirTemp("", "acp-dual-agent")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	binary = filepath.Join(dir, "dual-agent")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(filepath.Dir(filepath.Dir(currentFile)), "dual-agent")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("build dual-agent: %w", err)
	}
	return binary, cleanup, nil
}

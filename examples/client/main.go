// Command client is an interactive ACP client for any agent over stdio.
//
//	go run ./examples/client                        # the sibling agent example
//	go run ./examples/client go run ./examples/echo # any agent command
//
// It shows the client side of ACP: spawning an agent, driving turns with
// ClientSession and Turn, rendering updates with a type switch over the
// SessionUpdate union (render.go), cancelling a turn on Ctrl-C, answering
// permission requests, switching session modes, serving the optional file
// system methods and running commands in terminals for the agent
// (terminal.go), and calling a typed extension method with acp.CallExt.
package main

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp1"
)

// exampleClient implements acp1.Client, plus acp1.FileReader,
// acp1.FileWriter and, through the embedded terminals, acp1.TerminalHandler,
// which ClientCapabilitiesOf turns into the fs and terminal capabilities it
// advertises.
type exampleClient struct {
	*terminals
	// lines carries stdin a line at a time and is closed at its end. The
	// prompt loop and permission requests share it, so input typed ahead is
	// not lost, and a permission request can stop waiting for an answer.
	lines <-chan string

	mu sync.Mutex
	// toolTitles maps each tool call to its title: updates and permission
	// requests name a tool call by its id and carry only what changed.
	toolTitles map[acp1.ToolCallID]string
	// cancelled is closed when the user cancels the running turn.
	cancelled chan struct{}
}

func (c *exampleClient) setToolTitle(id acp1.ToolCallID, title string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.toolTitles[id] = title
}

func (c *exampleClient) toolTitle(id acp1.ToolCallID) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.toolTitles[id]
}

// readLines reads r a line at a time in the background.
func readLines(r io.Reader) <-chan string {
	lines := make(chan string)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	return lines
}

// SessionUpdate receives every update the agent sends. This client renders
// the updates of its own turns from Turn.Updates instead, so it has nothing
// to do here; a UI that shows background activity would update its state.
func (c *exampleClient) SessionUpdate(context.Context, *acp1.SessionNotification) error {
	return nil
}

// RequestPermission asks the user to choose an option. A client that
// cancels the turn must answer the requests still waiting as cancelled, so
// Ctrl-C stops the wait as well as the turn.
func (c *exampleClient) RequestPermission(ctx context.Context, params *acp1.RequestPermissionRequest) (*acp1.RequestPermissionResponse, error) {
	c.mu.Lock()
	cancelled := c.cancelled
	c.mu.Unlock()

	fmt.Printf("\n🔐 Permission requested: %s\n", cmp.Or(params.ToolCall.GetTitle(), c.toolTitle(params.ToolCall.ToolCallID)))
	c.renderContent(params.ToolCall.Content) // the change the agent proposes
	for i, option := range params.Options {
		fmt.Printf("   %d. %s (%s)\n", i+1, option.Name, option.Kind)
	}

	for {
		fmt.Print("\nChoose an option: ")
		var answer string
		select {
		case line, ok := <-c.lines:
			if !ok {
				return acp1.PermissionCancelled(), nil // stdin ended
			}
			answer = line
		case <-cancelled:
			fmt.Println("cancelled")
			return acp1.PermissionCancelled(), nil
		case <-ctx.Done():
			fmt.Println("withdrawn by the agent")
			return nil, ctx.Err()
		}
		choice, err := strconv.Atoi(strings.TrimSpace(answer))
		if err != nil || choice < 1 || choice > len(params.Options) {
			fmt.Printf("Enter a number between 1 and %d.\n", len(params.Options))
			continue
		}
		return acp1.PermissionSelected(params.Options[choice-1].OptionID), nil
	}
}

// ReadTextFile reads a file, or with Line and Limit only the lines from Line
// (1-based) on, at most Limit of them.
func (c *exampleClient) ReadTextFile(_ context.Context, params *acp1.ReadTextFileRequest) (*acp1.ReadTextFileResponse, error) {
	content, err := os.ReadFile(params.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, acp.ResourceNotFound(params.Path).WithData(map[string]string{"uri": params.Path})
	}
	if err != nil {
		return nil, err
	}
	text := string(content)
	if params.Line != nil || params.Limit != nil {
		lines := strings.SplitAfter(text, "\n")
		start := min(max(int(params.GetLine()), 1)-1, len(lines))
		end := len(lines)
		if params.Limit != nil {
			end = min(start+int(*params.Limit), end)
		}
		text = strings.Join(lines[start:end], "")
	}
	return &acp1.ReadTextFileResponse{Content: text}, nil
}

// WriteTextFile writes a file, creating it and its directory if needed.
func (c *exampleClient) WriteTextFile(_ context.Context, params *acp1.WriteTextFileRequest) (*acp1.WriteTextFileResponse, error) {
	if err := os.MkdirAll(filepath.Dir(params.Path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(params.Path, []byte(params.Content), 0o644); err != nil {
		return nil, err
	}
	return &acp1.WriteTextFileResponse{}, nil
}

// pingResult is the agent example's reply to its _example.com/ping extension.
type pingResult struct {
	Reply string `json:"reply"`
}

func main() {
	verbose := flag.Bool("v", false, "show the agent's stderr")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: client [-v] [agent command...]\n\nWithout a command, it builds and runs the sibling agent example.\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if err := run(context.Background(), flag.Args(), *verbose); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, command []string, verbose bool) error {
	if len(command) == 0 {
		binary, cleanup, err := buildAgent()
		if err != nil {
			return err
		}
		defer cleanup()
		command = []string{binary}
	}
	cmd := exec.Command(command[0], command[1:]...)
	if !verbose {
		cmd.Stderr = io.Discard // agent logs would interleave with the conversation
	}

	client := &exampleClient{
		terminals:  &terminals{},
		lines:      readLines(os.Stdin),
		toolTitles: map[acp1.ToolCallID]string{},
	}
	agent, err := acp1.SpawnAgent(ctx, cmd, func(*acp1.ClientSideConnection) acp1.Client {
		return client
	})
	if err != nil {
		return fmt.Errorf("spawn agent: %w", err)
	}
	defer agent.Close()

	initialized, err := agent.Initialize(ctx, &acp1.InitializeRequest{
		ClientCapabilities: acp1.ClientCapabilitiesOf(client),
		ClientInfo:         &acp1.Implementation{Name: "example-client", Version: "0.1.0"},
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	fmt.Printf("Connected to agent (protocol v%d)\n", initialized.ProtocolVersion)

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	// NewSession rather than StartSession: the response lists the modes.
	created, err := agent.NewSession(ctx, &acp1.NewSessionRequest{Cwd: cwd})
	if err != nil {
		return fmt.Errorf("new session: %w", err)
	}
	session := agent.Session(created.SessionID)
	fmt.Printf("Session %s. Type a message, /ping <text> to call the agent's\nextension method, Ctrl-C to cancel a turn, or Ctrl-D to quit.\n", session.ID)
	if modes := created.Modes; modes != nil {
		fmt.Printf("Mode: %s. Switch with /mode <id>:", modes.CurrentModeID)
		for _, mode := range modes.AvailableModes {
			fmt.Printf(" %s", mode.ID)
		}
		fmt.Println()
	}

	for {
		fmt.Print("\n> ")
		line, ok := <-client.lines
		if !ok {
			fmt.Println()
			return nil
		}
		line = strings.TrimSpace(line)
		message, isPing := strings.CutPrefix(line, "/ping ")
		mode, isMode := strings.CutPrefix(line, "/mode ")
		switch {
		case line == "":
		case isPing:
			result, err := acp.CallExt[pingResult](ctx, agent, "_example.com/ping", map[string]string{"message": message})
			if err != nil {
				fmt.Printf("ping: %v\n", err) // agents other than the example answer "method not found"
				continue
			}
			fmt.Println(result.Reply)
		case isMode:
			_, err := agent.SetSessionMode(ctx, &acp1.SetSessionModeRequest{SessionID: session.ID, ModeID: acp1.SessionModeID(mode)})
			if err != nil {
				fmt.Printf("mode: %v\n", err)
				continue
			}
			fmt.Printf("mode: %s\n", mode)
		default:
			if err := client.prompt(ctx, session, line); err != nil {
				return err
			}
		}
	}
}

// prompt runs one turn, rendering its updates as they arrive.
func (c *exampleClient) prompt(ctx context.Context, session *acp1.ClientSession, text string) error {
	cancelled := make(chan struct{})
	c.mu.Lock()
	c.cancelled = cancelled
	c.mu.Unlock()
	turn, err := session.Prompt(ctx, acp1.TextBlock(text))
	if err != nil {
		return fmt.Errorf("prompt: %w", err)
	}

	// While the turn runs, Ctrl-C asks the agent to stop instead of ending
	// the client, and answers a permission request still waiting as
	// cancelled; the turn then ends with StopReasonCancelled.
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	defer signal.Stop(interrupt)
	go func() {
		select {
		case <-interrupt:
			_ = session.Cancel(ctx)
			close(cancelled)
		case <-turn.Done():
		}
	}()

	for update := range turn.Updates() {
		c.render(update)
	}
	result, err := turn.Wait()
	if err != nil {
		return fmt.Errorf("prompt: %w", err)
	}
	fmt.Printf("\n[%s]\n", result.StopReason)
	return nil
}

// buildAgent compiles the sibling agent example into a temporary directory
// and returns its path.
func buildAgent() (binary string, cleanup func(), err error) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", nil, fmt.Errorf("cannot locate this source file")
	}
	dir, err := os.MkdirTemp("", "acp-example-agent")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	binary = filepath.Join(dir, "agent")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}

	fmt.Println("Building agent...")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(filepath.Dir(filepath.Dir(currentFile)), "agent")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("build agent: %w", err)
	}
	return binary, cleanup, nil
}

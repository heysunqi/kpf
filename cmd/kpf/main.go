// Command kpf is the entry point for the kpf CLI / TUI / daemon binary.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"kpf/internal/config"
	"kpf/internal/daemon"
	"kpf/internal/ipc"
	"kpf/internal/k8s"
	"kpf/internal/kubeconfig"
	"kpf/internal/state"
	"kpf/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
)

const version = "0.1.1"

// hiddenArg is the second argv value the parent process passes when
// re-executing itself to run as the actual daemon.
const hiddenArg = "__daemon__"

func main() {
	// Re-exec fast path: this is the background daemon itself.
	if len(os.Args) >= 2 && os.Args[1] == hiddenArg {
		runDaemonForeground()
		return
	}

	if len(os.Args) < 2 {
		runTUI()
		return
	}

	switch os.Args[1] {
	case "tui":
		runTUI()
	case "daemon":
		runDaemonCmd(os.Args[2:])
	case "forward":
		runForwardCmd(os.Args[2:])
	case "ls", "list":
		runList()
	case "stop":
		runStop(os.Args[2:])
	case "logs":
		runLogs(os.Args[2:])
	case "ping":
		runPing()
	case "namespaces", "ns":
		runNamespaces(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Printf("kpf %s\n", version)
	case "help", "--help", "-h":
		printHelp()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		printHelp()
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Print(`kpf — K8s port-forward TUI tool

Usage:
  kpf                   Launch the interactive TUI (auto-starts daemon)
  kpf tui               Same as above
  kpf daemon start      Start the background daemon
  kpf daemon stop       Stop the background daemon
  kpf daemon status     Show daemon status
  kpf forward start ... Start a forward from the CLI
  kpf ls                List active forwards
  kpf stop <id>         Stop a single forward
  kpf stop --all        Stop all forwards
  kpf logs <id>         Show / follow forward logs
  kpf ping              Check daemon reachability
  kpf namespaces PATH   List namespaces from a kubeconfig
  kpf version           Print version
  kpf help              This help

Environment:
  KPF_HOME              Override state directory (default ~/.local/share/kpf)
  KPF_SOCKET            Override daemon socket path
  KPF_KUBECONFIG_DIR    Override kubeconfig scan directory
  KPF_DEBUG             Set non-empty to enable daemon debug logging (JSON to log file)
`)
}

// ---------------------------------------------------------------------------
// TUI (placeholder — implemented in Phase 3)
// ---------------------------------------------------------------------------

func runTUI() {
	paths, err := resolvePaths()
	if err != nil {
		die("config", err)
	}
	// Make sure the daemon is up before the TUI starts talking to it.
	ensureDaemon(paths)
	model := tui.New(paths.Socket)
	prog := tea.NewProgram(model, tea.WithAltScreen())

	// Long-lived event watcher: when forwards change, poke the TUI to refresh.
	// Falls back to its own reconnect loop if the daemon drops.
	// watchCtx is cancelled when the TUI exits so the goroutine doesn't outlive
	// the program and write to a dead *tea.Program (which would otherwise leave
	// stale control sequences on the terminal after the alt-screen is restored).
	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()
	go watchForwards(prog, paths.Socket, watchCtx)

	if _, err := prog.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tui:", err)
		watchCancel()
		os.Exit(1)
	}
	// prog.Run returned → Bubble Tea has already restored the alt screen.
	// Cancel the watcher so its in-flight reconnect loop doesn't fire events
	// at a *tea.Program that has already been torn down.
	watchCancel()
}

// watchForwards opens a daemon event subscription and translates each event
// into a TUI message via prog.Send. Reconnects with backoff if the
// subscription drops (daemon restart, etc.). When stop is closed, the goroutine
// returns promptly without writing to prog.
func watchForwards(p *tea.Program, socket string, stop context.Context) {
	const reconnectDelay = 2 * time.Second
	for {
		if stop.Err() != nil {
			return
		}
		ctx, cancel := context.WithCancel(stop)
		client := ipc.NewClient(socket)
		ch, err := client.SubscribeEvents(ctx, ipc.MethodForwardEvents, map[string]bool{"ok": true})
		if err != nil {
			cancel()
			select {
			case <-stop.Done():
				return
			case <-time.After(reconnectDelay):
				continue
			}
		}
		evCh := ch
	stopLoop:
		for {
			select {
			case <-stop.Done():
				cancel()
				_ = client.Close()
				return
			case ev, ok := <-evCh:
				if !ok {
					break stopLoop
				}
				// Only forward.* events matter for the active view.
				if !strings.HasPrefix(ev.Event, "forward.") {
					continue
				}
				p.Send(tui.ForwardEventMsg{EventName: ev.Event, ForwardID: ev.ForwardID})
			}
		}
		cancel()
		_ = client.Close()
		select {
		case <-stop.Done():
			return
		case <-time.After(reconnectDelay):
		}
	}
}

// ensureDaemon starts the background daemon if it isn't already running.
// If the daemon is already up, this is a no-op.
func ensureDaemon(p config.Paths) {
	if alive, _ := checkAlive(p); alive {
		return
	}
	// Clean up any stale socket left by a crashed process.
	_ = os.Remove(p.Socket)

	exe, err := os.Executable()
	if err != nil {
		die("executable", err)
	}
	logF, err := os.OpenFile(p.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		die("open log", err)
	}
	cmd := exec.Command(exe, hiddenArg)
	cmd.Stdin = nil
	cmd.SysProcAttr = daemon.SysProcAttrForBackground()
	cmd.Stdout = logF
	cmd.Stderr = logF
	if err := cmd.Start(); err != nil {
		die("start daemon", err)
	}
	_ = cmd.Process.Release()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(p.Socket); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	die("daemon did not become ready in 5s", nil)
}

// ---------------------------------------------------------------------------
// Daemon control
// ---------------------------------------------------------------------------

func runDaemonCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: kpf daemon start|stop|status")
		os.Exit(2)
	}
	switch args[0] {
	case "start":
		daemonStart()
	case "stop":
		daemonStop()
	case "status":
		daemonStatus()
	default:
		fmt.Fprintf(os.Stderr, "unknown daemon subcommand: %s\n", args[0])
		os.Exit(2)
	}
}

func resolvePaths() (config.Paths, error) {
	return config.DefaultPaths()
}

func checkAlive(p config.Paths) (bool, state.DaemonInfo) {
	info, err := state.ReadDaemonFile(p.DaemonFile)
	if err != nil {
		return false, state.DaemonInfo{}
	}
	if err := syscall.Kill(info.PID, 0); err != nil {
		return false, state.DaemonInfo{}
	}
	return true, info
}

func daemonStart() {
	paths, err := resolvePaths()
	if err != nil {
		die("config", err)
	}
	if alive, info := checkAlive(paths); alive {
		// Make sure the socket actually exists; otherwise the record is stale.
		if _, err := os.Stat(paths.Socket); err == nil {
			fmt.Printf("daemon already running (pid=%d, socket=%s)\n", info.PID, info.Socket)
			return
		}
		_ = state.RemoveDaemonFile(paths.DaemonFile)
	}
	// Clean up stale socket file left by a crashed daemon.
	_ = os.Remove(paths.Socket)

	exe, err := os.Executable()
	if err != nil {
		die("executable", err)
	}
	cmd := exec.Command(exe, hiddenArg)
	cmd.Stdin = nil
	cmd.SysProcAttr = daemon.SysProcAttrForBackground()
	logF, err := os.OpenFile(paths.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		die("open log", err)
	}
	cmd.Stdout = logF
	cmd.Stderr = logF
	if err := cmd.Start(); err != nil {
		die("start daemon", err)
	}
	_ = cmd.Process.Release()

	// Poll for socket.
	deadline := time.Now().Add(5 * time.Second)
	var pid int
	for time.Now().Before(deadline) {
		if _, err := os.Stat(paths.Socket); err == nil {
			// Read daemon.json for the canonical pid (the re-execed process
			// is a grandchild — its pid is the one that wrote daemon.json).
			if info, err := state.ReadDaemonFile(paths.DaemonFile); err == nil {
				pid = info.PID
			} else {
				pid = cmd.Process.Pid
			}
			fmt.Printf("daemon started (pid=%d, socket=%s)\n", pid, paths.Socket)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	die("daemon did not become ready in 5s", nil)
}

func runDaemonForeground() {
	paths, err := resolvePaths()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	logF, err := os.OpenFile(paths.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "log:", err)
		os.Exit(1)
	}
	defer logF.Close()
	level := slog.LevelWarn
	if os.Getenv("KPF_DEBUG") != "" {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewJSONHandler(logF, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	info := state.DaemonInfo{
		PID:       os.Getpid(),
		Socket:    paths.Socket,
		StartedAt: time.Now().UTC(),
		Version:   version,
		LogFile:   paths.LogFile,
	}
	if err := state.WriteDaemonFile(paths.DaemonFile, info); err != nil {
		logger.Error("write daemon file", "err", err)
		os.Exit(1)
	}
	defer state.RemoveDaemonFile(paths.DaemonFile)
	defer os.Remove(paths.Socket)

	logger.Info("daemon starting", "pid", os.Getpid(), "socket", paths.Socket)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		sig := <-sigCh
		logger.Info("signal received, shutting down", "signal", sig.String())
		cancel()
	}()

	handler := daemon.NewHandler(paths, version, logger)
	server := &ipc.Server{
		Socket:  paths.Socket,
		Handler: handler.Methods(),
		Log:     logger,
	}
	if err := server.Serve(ctx); err != nil {
		logger.Error("server exited with error", "err", err)
		os.Exit(1)
	}
	logger.Info("daemon stopped cleanly")
}

func daemonStop() {
	paths, err := resolvePaths()
	if err != nil {
		die("config", err)
	}
	info, err := state.ReadDaemonFile(paths.DaemonFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("daemon not running")
			return
		}
		die("read daemon file", err)
	}
	if err := syscall.Kill(info.PID, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			fmt.Println("daemon process not found, cleaning up state")
			_ = state.RemoveDaemonFile(paths.DaemonFile)
			_ = os.Remove(paths.Socket)
			return
		}
		die("kill", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(paths.Socket); err != nil {
			fmt.Println("daemon stopped")
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "daemon did not stop in 5s; sending SIGKILL")
	_ = syscall.Kill(info.PID, syscall.SIGKILL)
}

func daemonStatus() {
	paths, err := resolvePaths()
	if err != nil {
		die("config", err)
	}
	if alive, info := checkAlive(paths); alive {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(info)
		return
	}
	fmt.Println("daemon not running")
	os.Exit(1)
}

// ---------------------------------------------------------------------------
// IPC client commands
// ---------------------------------------------------------------------------

func newClient() (*ipc.Client, config.Paths, error) {
	paths, err := resolvePaths()
	if err != nil {
		return nil, paths, err
	}
	return ipc.NewClient(paths.Socket), paths, nil
}

func runPing() {
	client, _, err := newClient()
	if err != nil {
		die("config", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		die("ping", err)
	}
	defer client.Close()
	var res ipc.PingResult
	if err := client.Call(ctx, ipc.MethodPing, nil, &res); err != nil {
		die("ping", err)
	}
	fmt.Printf("ok: version=%s uptime=%ds\n", res.Version, res.UptimeSec)
}

func runList() {
	client, _, err := newClient()
	if err != nil {
		die("config", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		die("connect", err)
	}
	defer client.Close()
	var res ipc.ListForwardsResult
	if err := client.Call(ctx, ipc.MethodForwardList, nil, &res); err != nil {
		die("list", err)
	}
	if len(res.Forwards) == 0 {
		fmt.Println("(no active forwards)")
		return
	}
	w := tabWriter(os.Stdout)
	fmt.Fprintln(w, "ID\tKUBECONFIG\tNAMESPACE\tKIND/OBJECT\tLOCAL→REMOTE\tSTATUS\tSTARTED")
	for _, f := range res.Forwards {
		ports := formatPorts(f.Ports)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s/%s\t%s\t%s\t%s\n",
			f.ID, truncate(f.Kubeconfig, 28), f.Namespace,
			strings.ToLower(f.Kind), f.Object,
			ports, f.Status, f.StartedAt)
	}
	w.Flush()
}

func runStop(args []string) {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	all := fs.Bool("all", false, "stop all forwards")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if !*all && fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: kpf stop <id>|--all")
		os.Exit(2)
	}
	client, _, err := newClient()
	if err != nil {
		die("config", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		die("connect", err)
	}
	defer client.Close()
	if *all {
		var res ipc.StopAllResult
		if err := client.Call(ctx, ipc.MethodForwardStopAll, nil, &res); err != nil {
			die("stopAll", err)
		}
		fmt.Printf("stopped %d forward(s)\n", res.StoppedCount)
		return
	}
	id := fs.Arg(0)
	params, _ := json.Marshal(ipc.StopForwardParams{ForwardID: id})
	var res ipc.StopForwardResult
	if err := client.Call(ctx, ipc.MethodForwardStop, params, &res); err != nil {
		die("stop", err)
	}
	fmt.Printf("stopped %s\n", id)
}

func runLogs(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: kpf logs <id>")
		os.Exit(2)
	}
	client, _, err := newClient()
	if err != nil {
		die("config", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		die("connect", err)
	}
	defer client.Close()
	// Forward.events streams everything; logs is just a filtered view we'll
	// implement when we have on-demand log subscriptions (Phase 6 polish).
	_ = client
	// For Phase 4, route the user through `forward.events`.
	fmt.Fprintln(os.Stderr, "use: kpf forward events <id>  (Phase 6: dedicated logs)")
}

// ---------------------------------------------------------------------------
// kpf forward <subcommand>
// ---------------------------------------------------------------------------

func runForwardCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: kpf forward start|list|stop|stopAll|events ...")
		os.Exit(2)
	}
	switch args[0] {
	case "start":
		runForwardStart(args[1:])
	case "list", "ls":
		runList()
	case "stop":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: kpf forward stop <id>")
			os.Exit(2)
		}
		runStop([]string{args[1]})
	case "stopAll":
		runStop([]string{"--all"})
	case "events":
		runForwardEvents(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown forward subcommand: %s\n", args[0])
		os.Exit(2)
	}
}

func runForwardStart(args []string) {
	fs := flag.NewFlagSet("forward start", flag.ExitOnError)
	kubeconfigPath := fs.String("kubeconfig", "", "path to kubeconfig file")
	contextName := fs.String("context", "", "kubeconfig context (default: current)")
	ns := fs.String("ns", "", "namespace")
	kind := fs.String("kind", "Pod", "resource kind (Pod|Service|Deployment|StatefulSet|ReplicaSet)")
	object := fs.String("object", "", "resource name")
	bind := fs.String("bind", "0.0.0.0", "local bind address")
	portsFlag := fs.String("ports", "", "port pairs (e.g. 8080:8080,9090:9092)")
	podName := fs.String("pod", "", "pod name (optional override)")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *kubeconfigPath == "" || *ns == "" || *object == "" || *portsFlag == "" {
		fmt.Fprintln(os.Stderr, "usage: kpf forward start --kubeconfig PATH --ns NS --kind K --object NAME [--bind 0.0.0.0] --ports local:remote[,local:remote...]")
		os.Exit(2)
	}
	ports, err := parsePorts(*portsFlag)
	if err != nil {
		die("parse --ports", err)
	}

	client, _, err := newClient()
	if err != nil {
		die("config", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		die("connect", err)
	}
	defer client.Close()

	params, _ := json.Marshal(ipc.StartForwardParams{
		Kubeconfig: *kubeconfigPath,
		Context:    *contextName,
		Namespace:  *ns,
		Kind:       *kind,
		Object:     *object,
		PodName:    *podName,
		Bind:       *bind,
		Ports:      ports,
	})
	var res ipc.StartForwardResult
	if err := client.Call(ctx, ipc.MethodForwardStart, params, &res); err != nil {
		die("start", err)
	}
	fmt.Printf("forward %s: local=%v remote=%v (pid handled by daemon)\n",
		res.ForwardID, res.LocalPorts, ports)
}

func runForwardEvents(args []string) {
	follow := false
	ids := []string{}
	for _, a := range args {
		if a == "-f" || a == "--follow" {
			follow = true
			continue
		}
		ids = append(ids, a)
	}
	if len(ids) == 0 {
		fmt.Fprintln(os.Stderr, "usage: kpf forward events [-f] <id> [id...]")
		os.Exit(2)
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}

	client, _, err := newClient()
	if err != nil {
		die("config", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := client.SubscribeEvents(ctx, ipc.MethodForwardEvents, map[string]bool{"ok": true})
	if err != nil {
		die("events", err)
	}
	go func() {
		for ev := range ch {
			if !want[ev.ForwardID] {
				continue
			}
			var p map[string]any
			_ = json.Unmarshal(ev.Payload, &p)
			stream, _ := p["stream"].(string)
			line, _ := p["line"].(string)
			fmt.Printf("%s\t%s\t%s\t%s\n", ev.Event, ev.ForwardID, stream, line)
		}
	}()
	if !follow {
		// Briefly sample then exit.
		time.Sleep(500 * time.Millisecond)
		return
	}
	c := make(chan struct{})
	go func() {
		buf := make([]byte, 1)
		for {
			if _, err := os.Stdin.Read(buf); err != nil {
				close(c)
				return
			}
		}
	}()
	<-c
}

func runNamespaces(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: kpf namespaces <kubeconfig-path> [context]")
		os.Exit(2)
	}
	path := args[0]
	contextName := ""
	if len(args) > 1 {
		contextName = args[1]
	}
	cfg, err := kubeconfig.Load(path, contextName)
	if err != nil {
		die("load kubeconfig", err)
	}
	cs, err := k8s.New(cfg)
	if err != nil {
		die("build client", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	nss, err := k8s.ListNamespaces(ctx, cs)
	if err != nil {
		die("list namespaces", err)
	}
	for _, n := range nss {
		fmt.Println(n)
	}
}

func die(msg string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", msg, err)
	} else {
		fmt.Fprintln(os.Stderr, msg)
	}
	os.Exit(1)
}

// tabWriter returns a tabwriter that aligns columns in CLI output.
func tabWriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
}

// formatPorts renders a slice of port maps as "local:remote local:remote".
func formatPorts(ps []ipc.PortMap) string {
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		parts = append(parts, fmt.Sprintf("%d:%d", p.Local, p.Remote))
	}
	return strings.Join(parts, ",")
}

// truncate shortens s to at most n runes, appending an ellipsis when cut.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 1 {
		return string(runes[:n])
	}
	return string(runes[:n-1]) + "…"
}

// parsePorts parses a comma-separated list of "local:remote" pairs.
func parsePorts(s string) ([]ipc.PortMap, error) {
	var out []ipc.PortMap
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		colon := strings.Index(item, ":")
		if colon <= 0 || colon == len(item)-1 {
			return nil, fmt.Errorf("bad port spec %q (want local:remote)", item)
		}
		l, err := strconv.Atoi(item[:colon])
		if err != nil {
			return nil, fmt.Errorf("bad local port in %q: %w", item, err)
		}
		r, err := strconv.Atoi(item[colon+1:])
		if err != nil {
			return nil, fmt.Errorf("bad remote port in %q: %w", item, err)
		}
		if l <= 0 || l > 65535 || r <= 0 || r > 65535 {
			return nil, fmt.Errorf("port out of range in %q", item)
		}
		out = append(out, ipc.PortMap{Local: l, Remote: r})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no port pairs in %q", s)
	}
	return out, nil
}

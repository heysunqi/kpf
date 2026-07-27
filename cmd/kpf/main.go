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
	"sort"
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
		runList(os.Args[2:])
	case "stop":
		runStop(os.Args[2:])
	case "restart":
		runRestart(os.Args[2:])
	case "logs":
		runLogs(os.Args[2:])
	case "ping":
		runPing()
	case "doctor":
		runDoctor()
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
  kpf forward restart <id>  Stop and re-start a forward with the same spec
  kpf ls                List active forwards (--json, --watch/-w, --ns/--kind/--status)
  kpf stop <id>         Stop a single forward
  kpf stop --all        Stop all forwards
  kpf restart <id>      Stop and re-start a forward with the same spec
  kpf logs [-f] <id>    Show / follow forward logs
  kpf ping              Check daemon reachability
  kpf doctor            Health check (daemon / socket / state / listener parity)
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
// TUI
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

func runList(args []string) {
	fs := flag.NewFlagSet("ls", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "output as JSON")
	watch := fs.Bool("watch", false, "watch mode (re-print on forward events)")
	watchShort := fs.Bool("w", false, "watch mode (shorthand)")
	nsFilter := fs.String("ns", "", "filter by namespace")
	kindFilter := fs.String("kind", "", "filter by resource kind")
	statusFilter := fs.String("status", "", "filter by status")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "usage: kpf ls [--json|-w|--watch] [--ns NS] [--kind KIND] [--status STATUS]")
		os.Exit(2)
	}
	if *watch || *watchShort {
		runListWatch(*jsonOut, *nsFilter, *kindFilter, *statusFilter)
		return
	}
	runListOnce(*jsonOut, *nsFilter, *kindFilter, *statusFilter)
}

func runListOnce(jsonOut bool, nsFilter, kindFilter, statusFilter string) {
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
	forwards := filterForwards(res.Forwards, nsFilter, kindFilter, statusFilter)
	sortForwardsByID(forwards)
	if jsonOut {
		printListJSON(forwards)
		return
	}
	if len(forwards) == 0 {
		fmt.Println("(no active forwards)")
		return
	}
	printListTable(forwards)
}

// runListWatch subscribes to forward events and re-renders the list whenever
// any state-change event arrives. Two ipc.Client instances are used because
// SubscribeEvents owns the underlying reader goroutine — a Call on the same
// client would race for ReadFrame.
func runListWatch(jsonOut bool, nsFilter, kindFilter, statusFilter string) {
	paths, err := resolvePaths()
	if err != nil {
		die("config", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	subClient := ipc.NewClient(paths.Socket)
	defer subClient.Close()
	ch, err := subClient.SubscribeEvents(ctx, ipc.MethodForwardEvents, map[string]bool{"ok": true})
	if err != nil {
		die("watch", err)
	}

	listClient := ipc.NewClient(paths.Socket)
	defer listClient.Close()

	refresh := func() {
		ctx2, cancel2 := context.WithTimeout(ctx, 5*time.Second)
		defer cancel2()
		var res ipc.ListForwardsResult
		if err := listClient.Call(ctx2, ipc.MethodForwardList, nil, &res); err != nil {
			fmt.Fprintf(os.Stderr, "list: %v\n", err)
			return
		}
		forwards := filterForwards(res.Forwards, nsFilter, kindFilter, statusFilter)
		sortForwardsByID(forwards)
		if jsonOut {
			// Stream snapshots as JSON lines (no clear-screen).
			data, _ := json.Marshal(forwards)
			fmt.Println(string(data))
			return
		}
		fmt.Print("\033[2J\033[H")
		printListTable(forwards)
	}
	refresh()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			// Log events fire on every log line — too high-frequency for a
			// state-change watch. Filter to ready/dropped/stopped/stale/error.
			switch ev.Event {
			case ipc.EventForwardReady, ipc.EventForwardDropped, ipc.EventForwardStopped,
				"forward.stale", "forward.error":
				refresh()
			}
		}
	}
}

// filterForwards applies --ns/--kind/--status filters. Empty filter strings
// are no-ops; comparisons are case-insensitive for kind and status.
func filterForwards(forwards []ipc.ForwardInfo, nsFilter, kindFilter, statusFilter string) []ipc.ForwardInfo {
	if nsFilter == "" && kindFilter == "" && statusFilter == "" {
		return forwards
	}
	out := make([]ipc.ForwardInfo, 0, len(forwards))
	for _, f := range forwards {
		if nsFilter != "" && f.Namespace != nsFilter {
			continue
		}
		if kindFilter != "" && !strings.EqualFold(f.Kind, kindFilter) {
			continue
		}
		if statusFilter != "" && !strings.EqualFold(f.Status, statusFilter) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// sortForwardsByID sorts the slice in place by ID ascending (lexicographic).
// Manager.List returns forwards in Go map iteration order (random), so this
// gives the CLI a stable, predictable row order regardless of arrival time.
func sortForwardsByID(forwards []ipc.ForwardInfo) {
	sort.Slice(forwards, func(i, j int) bool {
		return forwards[i].ID < forwards[j].ID
	})
}

// printListJSON emits the forwards as a JSON array (always non-nil). Suitable
// for piping into `jq`.
func printListJSON(forwards []ipc.ForwardInfo) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(forwards)
}

// printListTable emits forwards in tab-aligned columns. Caller is responsible
// for the (no rows) check.
func printListTable(forwards []ipc.ForwardInfo) {
	w := tabWriter(os.Stdout)
	fmt.Fprintln(w, "ID\tKUBECONFIG\tNAMESPACE\tKIND/OBJECT\tLOCAL→REMOTE\tSTATUS\tSTARTED")
	for _, f := range forwards {
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

func runRestart(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: kpf restart <id>")
		os.Exit(2)
	}
	id := args[0]
	client, _, err := newClient()
	if err != nil {
		die("config", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		die("connect", err)
	}
	defer client.Close()
	params, _ := json.Marshal(ipc.RestartForwardParams{ForwardID: id})
	var res ipc.RestartForwardResult
	if err := client.Call(ctx, ipc.MethodForwardRestart, params, &res); err != nil {
		die("restart", err)
	}
	fmt.Printf("restarted %s: local=%v\n", res.ForwardID, res.LocalPorts)
}

// knownStatuses is the set of valid forward.Status values. Used by doctor
// to flag any forward whose status doesn't match the lifecycle vocabulary.
var knownStatuses = map[string]bool{
	string("starting"): true, // forward.Status is unexported; constants are stringly
	string("ready"):    true,
	string("dropped"):  true,
	string("stopped"):  true,
	string("stale"):    true,
	string("error"):    true,
}

// runDoctor performs a series of health checks against the running daemon
// and exits with a status code reflecting the worst outcome:
//   0 — all PASS
//   1 — at least one WARN, no FAIL
//   2 — at least one FAIL
type doctorCheck struct {
	name   string
	level  int // 0=PASS, 1=WARN, 2=FAIL
	detail string
}

func runDoctor() {
	var results []doctorCheck

	// 1. Daemon reachability (covers socket + ping in one call).
	client, paths, err := newClient()
	if err != nil {
		results = append(results, doctorCheck{"config", 2, err.Error()})
		printDoctor(results)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		results = append(results, doctorCheck{"daemon-reachable", 2,
			fmt.Sprintf("socket %s: %v", paths.Socket, err)})
		printDoctor(results)
		os.Exit(2)
	}
	defer client.Close()

	var pingRes ipc.PingResult
	if err := client.Call(ctx, ipc.MethodPing, nil, &pingRes); err != nil {
		results = append(results, doctorCheck{"daemon-ping", 2, err.Error()})
	} else {
		results = append(results, doctorCheck{"daemon-ping", 0,
			fmt.Sprintf("version=%s uptime=%ds", pingRes.Version, pingRes.UptimeSec)})
	}

	// 2. Forward list (state.json health).
	var listRes ipc.ListForwardsResult
	if err := client.Call(ctx, ipc.MethodForwardList, nil, &listRes); err != nil {
		results = append(results, doctorCheck{"state-list", 2, err.Error()})
	} else {
		results = append(results, doctorCheck{"state-list", 0,
			fmt.Sprintf("%d forward(s) tracked", len(listRes.Forwards))})
	}

	// 3. Listener parity (live vs claimed ports).
	var liveRes ipc.LivePortsResult
	if err := client.Call(ctx, ipc.MethodForwardLivePorts, nil, &liveRes); err != nil {
		results = append(results, doctorCheck{"listener-parity", 2, err.Error()})
	} else {
		live := map[int]bool{}
		for _, p := range liveRes.Ports {
			live[p] = true
		}
		claimed := map[int]bool{}
		for _, f := range listRes.Forwards {
			if f.Status == "stopped" || f.Status == "stale" {
				continue
			}
			for _, p := range f.Ports {
				claimed[p.Local] = true
			}
		}
		var orphan, missing []int
		for p := range live {
			if !claimed[p] {
				orphan = append(orphan, p)
			}
		}
		for p := range claimed {
			if !live[p] {
				missing = append(missing, p)
			}
		}
		switch {
		case len(orphan) > 0 || len(missing) > 0:
			results = append(results, doctorCheck{"listener-parity", 1,
				fmt.Sprintf("orphan=%v missing=%v", orphan, missing)})
		default:
			results = append(results, doctorCheck{"listener-parity", 0,
				fmt.Sprintf("live=%v all claimed", sortedKeys(live))})
		}
	}

	// 4. Stale forward count.
	staleCount := 0
	for _, f := range listRes.Forwards {
		if f.Status == "stale" {
			staleCount++
		}
	}
	if staleCount > 0 {
		results = append(results, doctorCheck{"stale-forwards", 1,
			fmt.Sprintf("%d forward(s) marked stale (likely persistent 'not found' from backing pod)", staleCount)})
	} else {
		results = append(results, doctorCheck{"stale-forwards", 0, "none"})
	}

	// 5. Unknown status values.
	unknownStatuses := map[string]int{}
	for _, f := range listRes.Forwards {
		if !knownStatuses[f.Status] {
			unknownStatuses[f.Status]++
		}
	}
	if len(unknownStatuses) > 0 {
		results = append(results, doctorCheck{"status-vocabulary", 2,
			fmt.Sprintf("unexpected statuses: %v", unknownStatuses)})
	} else {
		results = append(results, doctorCheck{"status-vocabulary", 0, "all statuses recognized"})
	}

	printDoctor(results)

	worst := 0
	for _, r := range results {
		if r.level > worst {
			worst = r.level
		}
	}
	switch worst {
	case 2:
		os.Exit(2)
	case 1:
		os.Exit(1)
	}
}

// printDoctor writes `[LEVEL] name: detail` lines in check order. The level
// field is rendered as PASS/WARN/FAIL.
func printDoctor(results []doctorCheck) {
	for _, r := range results {
		var tag string
		switch r.level {
		case 0:
			tag = "PASS"
		case 1:
			tag = "WARN"
		case 2:
			tag = "FAIL"
		}
		fmt.Printf("[%s] %s: %s\n", tag, r.name, r.detail)
	}
}

// sortedKeys returns the sorted int keys of a set map (for stable doctor
// output in the listener-parity PASS line).
func sortedKeys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// insertion sort is fine for tiny slices (port counts are small).
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

func runLogs(args []string) {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	follow := fs.Bool("f", false, "follow log output (block until stdin closes)")
	followLong := fs.Bool("follow", false, "follow log output (block until stdin closes)")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: kpf logs [-f] <id>")
		os.Exit(2)
	}
	id := fs.Arg(0)
	doFollow := *follow || *followLong

	client, _, err := newClient()
	if err != nil {
		die("config", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	params, _ := json.Marshal(ipc.LogsParams{ForwardID: id, Follow: doFollow})
	ch, err := client.SubscribeEvents(ctx, ipc.MethodForwardLogs, params)
	if err != nil {
		die("logs", err)
	}
	go func() {
		for ev := range ch {
			if ev.Event != ipc.EventForwardLog {
				continue
			}
			var p map[string]any
			_ = json.Unmarshal(ev.Payload, &p)
			ts, _ := p["ts"].(string)
			stream, _ := p["stream"].(string)
			line, _ := p["line"].(string)
			fmt.Printf("%s\t%s\t%s\n", ts, stream, line)
		}
	}()
	if !doFollow {
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

// ---------------------------------------------------------------------------
// kpf forward <subcommand>
// ---------------------------------------------------------------------------

func runForwardCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: kpf forward start|list|stop|stopAll|restart|events ...")
		os.Exit(2)
	}
	switch args[0] {
	case "start":
		runForwardStart(args[1:])
	case "list", "ls":
		runList(args[1:])
	case "stop":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: kpf forward stop <id>")
			os.Exit(2)
		}
		runStop([]string{args[1]})
	case "stopAll":
		runStop([]string{"--all"})
	case "restart":
		runRestart(args[1:])
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

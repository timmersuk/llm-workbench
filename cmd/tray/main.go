// Command tray runs the exact same llm-workbench server as cmd/server
// in-process, alongside a Windows systray icon: a live health indicator
// (polling GET /healthcheck), an "Open in browser" action, and a "Quit"
// action that performs the same graceful shutdown cmd/server's Ctrl+C path
// does — no new shutdown-signal logic, see internal/serverapp.Run. This
// replaces the previous requirement that a windows-background-service
// operator remember a port number and open a browser tab by hand.
//
// Windows-first: this binary has no GOOS build tag (it compiles
// cross-platform), but its tray behavior is only designed, tested, and
// claimed on Windows — see docs/engineering conventions.md and this
// task's own plan for why.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"fyne.io/systray"
	"github.com/pkg/browser"
	"github.com/sirupsen/logrus"

	"github.com/timmersuk/llm-workbench/internal/serverapp"
)

// BuildID is set via -ldflags at build time (see Makefile), same
// convention as cmd/server/main.go's own BuildID var.
var BuildID = "dev"

// pollInterval is how often the tray polls GET /healthcheck to drive its
// icon/tooltip/menu. Any single missed poll immediately reverts the icon
// to StateUnreachable — there is no debounce (see state.go).
const pollInterval = 5 * time.Second

// pollHTTPTimeout bounds a single poll request so a hung connection can't
// stack up polls or delay noticing StateUnreachable past the next tick.
const pollHTTPTimeout = 3 * time.Second

func main() {
	cfg := serverapp.LoadConfig()
	cfg.BuildID = BuildID

	base, err := baseURL(cfg.HTTPAddr)
	if err != nil {
		logrus.WithError(err).WithField("httpAddr", cfg.HTTPAddr).
			Fatal("cannot derive a host/port to poll or browse to from HTTP_ADDR")
	}

	app := &trayApp{
		cfg:     cfg,
		baseURL: base,
		client:  &http.Client{Timeout: pollHTTPTimeout},
	}

	// systray.Run blocks until systray.Quit() is called (from
	// app.onQuitClicked, below), which only happens after the server has
	// fully shut down — so the icon is never removed before that.
	systray.Run(app.onReady, app.onExit)
}

// trayApp holds everything the tray's onReady callback and its background
// goroutines (poll loop, Open-in-browser click handler, Quit handler, exit
// watcher) share.
type trayApp struct {
	cfg     serverapp.Config
	baseURL string
	client  *http.Client

	cancel context.CancelFunc

	mu           sync.Mutex
	quitClicked  bool // true once the Quit menu item fires, so serverExited's watcher never reports a crash for an intentional shutdown
	serverErr    error
	serverExited chan struct{} // closed exactly once, when serverapp.Run returns, however it returns
}

// onReady starts the in-process server, builds the tray menu, and launches
// every background goroutine that drives it. Required by systray.Run's
// signature; systray itself calls this once, on its own event-loop
// goroutine, before returning control to main.
func (a *trayApp) onReady() {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.serverExited = make(chan struct{})

	systray.SetIcon(iconFor(StateUnreachable))
	systray.SetTooltip(formatTooltip(StateUnreachable, "starting"))

	statusItem := systray.AddMenuItem("Status: starting…", "")
	statusItem.Disable()
	systray.AddSeparator()
	openItem := systray.AddMenuItem("Open in browser", "Open "+a.baseURL+"/ in your browser")
	openItem.Disable()
	quitItem := systray.AddMenuItem("Quit", "Shut down llm-workbench and remove this icon")

	go a.runServer(ctx, statusItem, openItem)
	go a.pollLoop(ctx, statusItem, openItem)
	go a.handleOpenClicks(openItem)
	go a.handleQuitClicked(quitItem, statusItem, openItem)
}

// onExit runs after systray.Quit() has torn down the native icon. Required
// by systray.Run's signature; there is nothing left to clean up here since
// handleQuitClicked already waited for the server to fully shut down
// before calling systray.Quit().
func (a *trayApp) onExit() {}

// runServer runs the exact same server cmd/server does, in-process. Its
// only two possible endings are: ctx cancelled by handleQuitClicked (a
// graceful shutdown, serverErr nil), or an unexpected early return (a real
// crash, e.g. gitstore.Open failing) — either way, serverExited is closed
// exactly once so every other goroutine that depends on "has the server
// stopped yet" (handleQuitClicked, the poll loop) can observe it.
func (a *trayApp) runServer(ctx context.Context, statusItem, openItem *systray.MenuItem) {
	err := serverapp.Run(ctx, a.cfg)

	a.mu.Lock()
	a.serverErr = err
	crashed := !a.quitClicked
	a.mu.Unlock()
	close(a.serverExited)

	if !crashed {
		return
	}

	detail := "server exited unexpectedly"
	if err != nil {
		detail = err.Error()
		logrus.WithError(err).Error("server exited unexpectedly")
	} else {
		logrus.Warn(detail)
	}
	a.setState(StateUnreachable, detail, statusItem, openItem)
}

// pollLoop drives the tray's icon/tooltip/menu from GET /healthcheck,
// starting with an immediate poll (so the icon isn't stuck on "starting"
// for a full pollInterval) and then repeating every pollInterval until ctx
// is cancelled (Quit) or the server has already exited (crash, handled by
// runServer instead — polling stops rather than racing it).
func (a *trayApp) pollLoop(ctx context.Context, statusItem, openItem *systray.MenuItem) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	a.pollOnce(ctx, statusItem, openItem)
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.serverExited:
			return
		case <-ticker.C:
			a.pollOnce(ctx, statusItem, openItem)
		}
	}
}

// pollOnce issues a single GET /healthcheck and updates the tray
// accordingly. Any failure to obtain and decode a response — a transport
// error, a non-200/503 status, or a malformed body — is treated as
// StateUnreachable without ever calling stateFromHealthcheck; only a
// successfully decoded body is classified Healthy/Degraded by it.
func (a *trayApp) pollOnce(ctx context.Context, statusItem, openItem *systray.MenuItem) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/healthcheck", nil)
	if err != nil {
		a.setState(StateUnreachable, err.Error(), statusItem, openItem)
		return
	}

	resp, err := a.client.Do(req)
	if err != nil {
		a.setState(StateUnreachable, err.Error(), statusItem, openItem)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		a.setState(StateUnreachable, resp.Status, statusItem, openItem)
		return
	}

	var payload healthcheckPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		a.setState(StateUnreachable, "malformed /healthcheck response: "+err.Error(), statusItem, openItem)
		return
	}

	a.setState(stateFromHealthcheck(payload), payload.Error, statusItem, openItem)
}

// setState applies state/detail to the icon, tooltip, and status menu
// item, and enables/disables "Open in browser" — enabled only when
// Healthy, per this task's success criteria.
func (a *trayApp) setState(state State, detail string, statusItem, openItem *systray.MenuItem) {
	systray.SetIcon(iconFor(state))
	systray.SetTooltip(formatTooltip(state, detail))
	statusItem.SetTitle("Status: " + state.String())

	if state == StateHealthy {
		openItem.Enable()
	} else {
		openItem.Disable()
	}
}

// handleOpenClicks opens the server's root URL in the user's default
// browser each time "Open in browser" is clicked. The menu item is only
// enabled while StateHealthy (see setState), but the check is repeated
// here too since a click can race a poll that just flipped it back off.
func (a *trayApp) handleOpenClicks(openItem *systray.MenuItem) {
	for range openItem.ClickedCh {
		if openItem.Disabled() {
			continue
		}
		if err := browser.OpenURL(a.baseURL + "/"); err != nil {
			logrus.WithError(err).WithField("url", a.baseURL+"/").Warn("failed to open browser")
		}
	}
}

// handleQuitClicked performs the full graceful shutdown on Quit: cancel
// the server's ctx, wait for serverapp.Run to actually return (bounded by
// cfg.ShutdownTimeout plus a margin for the wait itself), then remove the
// tray icon. If the server has already exited unexpectedly before Quit was
// ever clicked (runServer already closed serverExited), this returns
// immediately — there is nothing left to shut down.
func (a *trayApp) handleQuitClicked(quitItem, statusItem, openItem *systray.MenuItem) {
	<-quitItem.ClickedCh

	a.mu.Lock()
	a.quitClicked = true
	a.mu.Unlock()

	quitItem.Disable()
	openItem.Disable()
	a.setState(StateUnreachable, "stopping…", statusItem, openItem)

	a.cancel()

	select {
	case <-a.serverExited:
	case <-time.After(a.cfg.ShutdownTimeout + 5*time.Second):
		logrus.Warn("timed out waiting for the server to shut down; removing tray icon anyway")
	}

	systray.Quit()
}

//go:build browser

package browser_test

import (
	"context"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	homelab "github.com/binhminh/HomeLab-Minh"
	"github.com/binhminh/HomeLab-Minh/internal/httpapi"
	"github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

type layoutReport struct {
	ScrollWidth     float64  `json:"scrollWidth"`
	ViewportWidth   float64  `json:"viewportWidth"`
	LeftX           float64  `json:"leftX"`
	LeftY           float64  `json:"leftY"`
	LeftBottom      float64  `json:"leftBottom"`
	CenterX         float64  `json:"centerX"`
	CenterY         float64  `json:"centerY"`
	CenterBottom    float64  `json:"centerBottom"`
	RightX          float64  `json:"rightX"`
	RightY          float64  `json:"rightY"`
	SystemColumns   int      `json:"systemColumns"`
	ServicesColumns int      `json:"servicesColumns"`
	Services        int      `json:"services"`
	Containers      int      `json:"containers"`
	TerminalHeight  float64  `json:"terminalHeight"`
	ToolbarHeight   float64  `json:"toolbarHeight"`
	TerminalBody    float64  `json:"terminalBody"`
	Collapsed       bool     `json:"collapsed"`
	ToggleExpanded  string   `json:"toggleExpanded"`
	HasXterm        bool     `json:"hasXterm"`
	Overflow        []string `json:"overflow"`
	TouchFailures   []string `json:"touchFailures"`
	A11yFailures    []string `json:"a11yFailures"`
}

func TestDemoDashboardResponsiveColdLoad(t *testing.T) {
	chrome := chromePath(t)
	assets, err := homelab.Static()
	if err != nil {
		t.Fatal(err)
	}
	static, err := httpapi.NewStaticHandler(assets)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(static)
	defer server.Close()
	serverURL, _ := url.Parse(server.URL)

	viewports := []struct {
		name          string
		width, height int64
	}{
		{"phone-320", 320, 700},
		{"phone-390", 390, 844},
		{"mobile-640", 640, 844},
		{"mobile-max-767", 767, 900},
		{"workbench-768", 768, 900},
		{"workbench-max-899", 899, 900},
		{"tablet-min-900", 900, 900},
		{"tablet-1024", 1024, 900},
		{"tablet-max-1279", 1279, 900},
		{"desktop-min-1280", 1280, 900},
		{"desktop-1440", 1440, 900},
		{"wide-1920", 1920, 1080},
	}
	allocatorOptions := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chrome), chromedp.Flag("no-sandbox", true), chromedp.Flag("disable-gpu", true))
	allocator, cancelAllocator := chromedp.NewExecAllocator(context.Background(), allocatorOptions...)
	defer cancelAllocator()

	for _, viewport := range viewports {
		viewport := viewport
		t.Run(viewport.name, func(t *testing.T) {
			ctx, cancel := chromedp.NewContext(allocator)
			defer cancel()
			ctx, timeout := context.WithTimeout(ctx, 20*time.Second)
			defer timeout()

			var mu sync.Mutex
			failures := make([]string, 0)
			chromedp.ListenTarget(ctx, func(event any) {
				mu.Lock()
				defer mu.Unlock()
				switch value := event.(type) {
				case *cdpruntime.EventExceptionThrown:
					failures = append(failures, value.ExceptionDetails.Text)
				case *cdpruntime.EventConsoleAPICalled:
					if value.Type == cdpruntime.APITypeError {
						failures = append(failures, "console.error")
					}
				case *network.EventRequestWillBeSent:
					requested, parseErr := url.Parse(value.Request.URL)
					if parseErr == nil && (requested.Scheme == "http" || requested.Scheme == "https" || requested.Scheme == "ws" || requested.Scheme == "wss") && requested.Host != serverURL.Host {
						failures = append(failures, "external request: "+value.Request.URL)
					}
				}
			})

			var report layoutReport
			err := chromedp.Run(ctx,
				network.Enable(),
				chromedp.EmulateViewport(viewport.width, viewport.height),
				chromedp.Navigate(server.URL+"/?demo=1"),
				chromedp.WaitVisible("#dashboard", chromedp.ByQuery),
				chromedp.WaitReady("#terminal-size-compact", chromedp.ByQuery),
				chromedp.WaitVisible(".service-card:not(.skeleton-card)", chromedp.ByQuery),
				chromedp.Evaluate(`(() => {
          const rect = (selector) => document.querySelector(selector).getBoundingClientRect();
          const left = rect('.column-left');
          const center = rect('.column-center');
          const right = rect('.column-right');
          return {
            scrollWidth: document.documentElement.scrollWidth,
            viewportWidth: window.innerWidth,
            leftX: left.x, leftY: left.y, leftBottom: left.bottom,
            centerX: center.x, centerY: center.y, centerBottom: center.bottom,
            rightX: right.x, rightY: right.y,
            systemColumns: getComputedStyle(document.querySelector('.system-panel')).gridTemplateColumns.split(' ').filter(Boolean).length,
            servicesColumns: getComputedStyle(document.querySelector('.services-grid')).gridTemplateColumns.split(' ').filter(Boolean).length,
            services: document.querySelectorAll('.service-card:not(.skeleton-card)').length,
            containers: document.querySelectorAll('.container-item:not(.skeleton-container)').length,
            terminalHeight: rect('#terminal-panel').height,
            toolbarHeight: rect('#terminal-panel .terminal-toolbar').height,
            terminalBody: rect('#terminal-body').height,
            collapsed: document.querySelector('#terminal-panel').classList.contains('is-collapsed'),
            toggleExpanded: document.querySelector('#terminal-toggle').getAttribute('aria-expanded'),
            hasXterm: Boolean(document.querySelector('.xterm .xterm-helper-textarea')),
            overflow: [...document.querySelectorAll('body *')]
              .filter((node) => {
                if (node.closest('.xterm')) return false;
                const style = getComputedStyle(node);
                const bounds = node.getBoundingClientRect();
                return style.display !== 'none' && style.visibility !== 'hidden' && bounds.width > 0
                  && (bounds.right > window.innerWidth + 1 || bounds.left < -1);
              })
              .slice(0, 10)
              .map((node) => node.tagName.toLowerCase() + '#' + node.id + '.' + String(node.className)),
            touchFailures: [...document.querySelectorAll('#focus-add-service, .service-link, .service-menu-button, .container-action, #terminal-toggle, #terminal-host-shell, .icon-button')]
              .filter((node) => {
                const style = getComputedStyle(node);
                const bounds = node.getBoundingClientRect();
                return style.display !== 'none' && style.visibility !== 'hidden' && bounds.width > 0
                  && (bounds.width < 44 || bounds.height < 44);
              })
              .map((node) => node.id || node.className),
            a11yFailures: [
              ...(getComputedStyle(document.querySelector('#freshness-text')).display === 'none' ? ['connection status text hidden'] : []),
              ...(!document.querySelector('#freshness').getAttribute('aria-label') ? ['connection status label missing'] : []),
              ...(!document.querySelector('#terminal-toggle').getAttribute('aria-label') ? ['terminal toggle label missing'] : []),
              ...(document.querySelector('#offline-banner').hasAttribute('aria-live') ? ['offline banner announces twice'] : [])
            ]
          };
        })()`, &report),
			)
			if err != nil {
				t.Fatal(err)
			}
			if viewport.width == 390 {
				var mobileWorkbench struct {
					Active    bool    `json:"active"`
					Top       float64 `json:"top"`
					Right     float64 `json:"right"`
					Bottom    float64 `json:"bottom"`
					Body      float64 `json:"body"`
					Collapsed bool    `json:"collapsed"`
				}
				err = chromedp.Run(ctx,
					chromedp.Click("#terminal-toggle", chromedp.ByQuery),
					chromedp.Evaluate(`(() => {
            const panel = document.querySelector('#terminal-panel');
            const bounds = panel.getBoundingClientRect();
            return {
              active: panel.classList.contains('is-mobile-workbench'),
              top: bounds.top,
              right: bounds.right,
              bottom: bounds.bottom,
              body: document.querySelector('#terminal-body').getBoundingClientRect().height,
              collapsed: panel.classList.contains('is-collapsed')
            };
          })()`, &mobileWorkbench),
					chromedp.Click("#terminal-toggle", chromedp.ByQuery),
				)
				if err != nil {
					t.Fatal(err)
				}
				if !mobileWorkbench.Active || mobileWorkbench.Collapsed || mobileWorkbench.Top > 57 || mobileWorkbench.Right < 389 || mobileWorkbench.Bottom < 843 || mobileWorkbench.Body < 700 {
					t.Fatalf("mobile terminal is not a full-screen workbench below the header: %+v", mobileWorkbench)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if len(failures) > 0 {
				t.Fatalf("browser failures: %s", strings.Join(failures, "; "))
			}
			if report.ScrollWidth > report.ViewportWidth+1 {
				t.Fatalf("horizontal overflow: %+v", report)
			}
			if len(report.Overflow) > 0 {
				t.Fatalf("visible controls overflow the viewport: %+v", report)
			}
			if len(report.A11yFailures) > 0 {
				t.Fatalf("responsive accessibility regression: %+v", report)
			}
			if report.Services < 2 || report.Containers < 3 || !report.HasXterm {
				t.Fatalf("demo acceptance data/xterm missing: %+v", report)
			}
			if !report.Collapsed || report.ToggleExpanded != "false" || report.TerminalBody != 0 || report.TerminalHeight < 40 || report.TerminalHeight > 52 || report.ToolbarHeight < 40 || report.ToolbarHeight > 52 {
				t.Fatalf("cold terminal must be a collapsed 44px-ish status bar: %+v", report)
			}
			switch {
			case viewport.width >= 1280:
				if !(report.LeftX < report.CenterX && report.CenterX < report.RightX && near(report.LeftY, report.CenterY, 4) && near(report.CenterY, report.RightY, 4)) {
					t.Fatalf("desktop columns are not one ordered row: %+v", report)
				}
			case viewport.width >= 900:
				if !(near(report.LeftY, report.CenterY, 4) && report.LeftX < report.CenterX && report.RightY >= report.LeftBottom-1 && near(report.LeftX, report.RightX, 4)) || report.SystemColumns != 1 {
					t.Fatalf("tablet layout is not system/services plus a full-width container list: %+v", report)
				}
			default:
				if len(report.TouchFailures) > 0 {
					t.Fatalf("sub-900 touch targets are smaller than 44px: %+v", report)
				}
				expectedServiceColumns := 1
				if viewport.width >= 520 {
					expectedServiceColumns = 2
				}
				if !(report.CenterY >= report.LeftBottom-1 && report.RightY >= report.CenterBottom-1) || report.ServicesColumns != expectedServiceColumns || report.SystemColumns != 2 {
					t.Fatalf("sub-900 layout is not a one-column stack: %+v", report)
				}
			}
		})
	}
}

type interactionReport struct {
	CPUText              string  `json:"cpuText"`
	CPUMax               string  `json:"cpuMax"`
	CPUProgress          string  `json:"cpuProgress"`
	CPUDetail            string  `json:"cpuDetail"`
	RAMText              string  `json:"ramText"`
	RAMOverLimit         bool    `json:"ramOverLimit"`
	DiskWarningVisible   bool    `json:"diskWarningVisible"`
	DiskLevel            string  `json:"diskLevel"`
	CrashedContainers    int     `json:"crashedContainers"`
	StoppedContainers    int     `json:"stoppedContainers"`
	Services             int     `json:"services"`
	AddedEndpoint        string  `json:"addedEndpoint"`
	KeyboardURL          string  `json:"keyboardUrl"`
	NativeServiceLink    bool    `json:"nativeServiceLink"`
	DialogInitialFocus   bool    `json:"dialogInitialFocus"`
	DialogRestoredFocus  bool    `json:"dialogRestoredFocus"`
	MenuInitialItem      string  `json:"menuInitialItem"`
	MenuSecondItem       string  `json:"menuSecondItem"`
	MenuRestoredFocus    bool    `json:"menuRestoredFocus"`
	StatusHasText        bool    `json:"statusHasText"`
	ListsAreQuiet        bool    `json:"listsAreQuiet"`
	FocusPreserved       bool    `json:"focusPreserved"`
	CompactHeight        float64 `json:"compactHeight"`
	DefaultHeight        float64 `json:"defaultHeight"`
	Maximized            bool    `json:"maximized"`
	MaximizePressed      string  `json:"maximizePressed"`
	TerminalSession      string  `json:"terminalSession"`
	TerminalOutput       string  `json:"terminalOutput"`
	CollapsedHeight      float64 `json:"collapsedHeight"`
	TerminalModeLogs     bool    `json:"terminalModeLogs"`
	InvokerFocusRestored bool    `json:"invokerFocusRestored"`
	DisconnectIsVisible  bool    `json:"disconnectIsVisible"`
}

func TestDemoDashboardInteractionsAndEdgeStates(t *testing.T) {
	chrome := chromePath(t)
	assets, err := homelab.Static()
	if err != nil {
		t.Fatal(err)
	}
	static, err := httpapi.NewStaticHandler(assets)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(static)
	defer server.Close()

	allocatorOptions := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chrome), chromedp.Flag("no-sandbox", true), chromedp.Flag("disable-gpu", true))
	allocator, cancelAllocator := chromedp.NewExecAllocator(context.Background(), allocatorOptions...)
	defer cancelAllocator()
	ctx, cancel := chromedp.NewContext(allocator)
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, 25*time.Second)
	defer timeout()

	var failures []string
	var mu sync.Mutex
	chromedp.ListenTarget(ctx, func(event any) {
		mu.Lock()
		defer mu.Unlock()
		switch value := event.(type) {
		case *cdpruntime.EventExceptionThrown:
			failures = append(failures, value.ExceptionDetails.Text)
		case *cdpruntime.EventConsoleAPICalled:
			if value.Type == cdpruntime.APITypeError {
				failures = append(failures, "console.error")
			}
		}
	})

	var report interactionReport
	err = chromedp.Run(ctx,
		chromedp.EmulateViewport(1440, 900),
		chromedp.Navigate(server.URL+"/?demo=1&edge=1"),
		chromedp.WaitVisible(".service-card:not(.skeleton-card)", chromedp.ByQuery),
		chromedp.WaitReady(".xterm", chromedp.ByQuery),
		chromedp.Click("#focus-add-service", chromedp.ByQuery),
		chromedp.WaitVisible("#service-dialog", chromedp.ByQuery),
		chromedp.Evaluate(`document.activeElement === document.querySelector('#service-form input[name="name"]')`, &report.DialogInitialFocus),
		chromedp.KeyEvent("\x1b"),
		chromedp.Poll(`!document.querySelector('#service-dialog').open`, nil, chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Sleep(50*time.Millisecond),
		chromedp.Evaluate(`document.activeElement === document.querySelector('#focus-add-service')`, &report.DialogRestoredFocus),
		chromedp.Click("#focus-add-service", chromedp.ByQuery),
		chromedp.WaitVisible("#service-dialog", chromedp.ByQuery),
		chromedp.SetValue(`#service-form input[name="name"]`, "Port service", chromedp.ByQuery),
		chromedp.SetValue(`#service-form input[name="displayUrl"]`, "9090", chromedp.ByQuery),
		chromedp.Click(`#service-form button[type="submit"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.service-card[data-service-id^="demo-"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
          const link = document.querySelector('.service-card .service-link');
          window.__keyboardService = '';
          link.addEventListener('click', (event) => {
            event.preventDefault();
            window.__keyboardService = event.currentTarget.href;
          }, { once: true });
          link.focus();
        })()`, nil),
		chromedp.KeyEvent("\r"),
		chromedp.ActionFunc(func(context.Context) error { t.Log("before service menu focus"); return nil }),
		chromedp.Focus(".service-menu-button", chromedp.ByQuery),
		chromedp.ActionFunc(func(context.Context) error { t.Log("after service menu focus"); return nil }),
		chromedp.KeyEvent("\r"),
		chromedp.WaitVisible("#context-menu", chromedp.ByQuery),
		chromedp.Poll(`document.activeElement?.getAttribute('role') === 'menuitem'`, nil, chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Evaluate(`document.activeElement?.textContent || ''`, &report.MenuInitialItem),
		chromedp.KeyEvent(kb.ArrowDown),
		chromedp.Evaluate(`document.activeElement?.textContent || ''`, &report.MenuSecondItem),
		chromedp.KeyEvent("\x1b"),
		chromedp.Sleep(50*time.Millisecond),
		chromedp.Evaluate(`document.activeElement?.classList.contains('service-menu-button') === true && document.querySelector('#context-menu').hidden`, &report.MenuRestoredFocus),
		chromedp.ActionFunc(func(context.Context) error { t.Log("before service link focus"); return nil }),
		chromedp.Focus(".service-link", chromedp.ByQuery),
		chromedp.ActionFunc(func(context.Context) error { t.Log("after service link focus"); return nil }),
		chromedp.Poll(`document.activeElement?.classList.contains('service-link') === true`, nil, chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Sleep(2200*time.Millisecond),
		chromedp.Evaluate(`document.activeElement?.classList.contains('service-link') === true && document.activeElement?.isConnected === true`, &report.FocusPreserved),
		chromedp.Click("#terminal-toggle", chromedp.ByQuery),
		chromedp.Click("#terminal-size-compact", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('#terminal-body').getBoundingClientRect().height`, &report.CompactHeight),
		chromedp.Click("#terminal-size-default", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('#terminal-body').getBoundingClientRect().height`, &report.DefaultHeight),
		chromedp.Click("#terminal-maximize", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('#terminal-panel').classList.contains('is-maximized')`, &report.Maximized),
		chromedp.Evaluate(`document.querySelector('#terminal-maximize').getAttribute('aria-pressed')`, &report.MaximizePressed),
		chromedp.Click("#terminal-maximize", chromedp.ByQuery),
		chromedp.Click("#terminal-toggle", chromedp.ByQuery),
		chromedp.ActionFunc(func(context.Context) error { t.Log("before container action focus"); return nil }),
		chromedp.Focus(".container-item .container-action:not(:disabled)", chromedp.ByQuery),
		chromedp.ActionFunc(func(context.Context) error { t.Log("after container action focus"); return nil }),
		chromedp.Evaluate(`window.__terminalInvoker = document.activeElement`, nil),
		chromedp.KeyEvent("\r"),
		chromedp.WaitVisible("#terminal-disconnect", chromedp.ByQuery),
		chromedp.Sleep(150*time.Millisecond),
		chromedp.Click("#terminal-toggle", chromedp.ByQuery),
		chromedp.Sleep(250*time.Millisecond),
		chromedp.Evaluate(`document.querySelector('#terminal-body').getBoundingClientRect().height`, &report.CollapsedHeight),
		chromedp.Evaluate(`document.activeElement === window.__terminalInvoker`, &report.InvokerFocusRestored),
		chromedp.Evaluate(`(() => ({
          cpuText: document.querySelector('#cpu-percent').textContent,
          cpuMax: document.querySelector('#cpu-progress').getAttribute('aria-valuemax'),
          cpuProgress: document.querySelector('#cpu-progress').style.getPropertyValue('--progress'),
          cpuDetail: document.querySelector('#cpu-detail').textContent,
          ramText: document.querySelector('#ram-percent').textContent,
          ramOverLimit: document.querySelector('#ram-percent').classList.contains('is-over-limit'),
          diskWarningVisible: !document.querySelector('#disk-warning').hidden,
          diskLevel: document.querySelector('#disk-progress').dataset.level,
          crashedContainers: document.querySelectorAll('.container-state[data-state="crashed"]').length,
          stoppedContainers: document.querySelectorAll('.container-state[data-state="stopped"]').length,
          services: document.querySelectorAll('.service-card:not(.skeleton-card)').length,
          addedEndpoint: [...document.querySelectorAll('.service-card')].find((node) => node.textContent.includes('Port service'))?.querySelector('.service-endpoint')?.textContent || '',
          keyboardUrl: window.__keyboardService || '',
          nativeServiceLink: [...document.querySelectorAll('.service-link')].every((link) => link.matches('a[href][target="_blank"]') && link.rel.split(/\s+/).includes('noopener')),
          statusHasText: [...document.querySelectorAll('.service-status, .container-state')].every((node) => node.textContent.trim().length > 0),
          listsAreQuiet: ['services-grid', 'containers-list', 'alerts-list'].every((id) => !document.querySelector('#' + id).hasAttribute('aria-live')),
          terminalSession: document.querySelector('#terminal-session-label').textContent,
          terminalOutput: document.querySelector('.xterm-rows')?.textContent || '',
          terminalModeLogs: document.querySelector('#terminal-panel').classList.contains('terminal-mode-logs'),
          disconnectIsVisible: !document.querySelector('#terminal-disconnect').hidden
        }))()`, &report),
	)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(failures) > 0 {
		t.Fatalf("browser failures: %s", strings.Join(failures, "; "))
	}
	if report.CPUText != "160.0%" || report.CPUMax != "800.0" || report.CPUProgress != "20.00" || !strings.Contains(report.CPUDetail, "🔥") {
		t.Fatalf("multi-core/temperature edge state failed: %+v", report)
	}
	if !report.RAMOverLimit || !strings.Contains(report.RAMText, "⚠") {
		t.Fatalf("memory overflow state failed: %+v", report)
	}
	if !report.DiskWarningVisible || report.DiskLevel != "hot" {
		t.Fatalf("disk warning state failed: %+v", report)
	}
	if report.CrashedContainers < 1 || report.StoppedContainers < 1 {
		t.Fatalf("container status edge states failed: %+v", report)
	}
	if report.Services != 5 || !strings.HasSuffix(report.AddedEndpoint, ":9090") {
		t.Fatalf("port-only quick add failed: %+v", report)
	}
	if !report.DialogInitialFocus || !report.DialogRestoredFocus {
		t.Fatalf("add-service dialog did not focus its first field or restore its invoker: %+v", report)
	}
	if !report.NativeServiceLink || !strings.Contains(report.KeyboardURL, "homelab.ts.net") {
		t.Fatalf("service is not a native keyboard-activatable link: %+v", report)
	}
	if strings.TrimSpace(report.MenuInitialItem) != "Edit service" || strings.TrimSpace(report.MenuSecondItem) != "Copy URL" || !report.MenuRestoredFocus {
		t.Fatalf("service overflow menu keyboard navigation failed: %+v", report)
	}
	if !report.StatusHasText || !report.ListsAreQuiet || !report.FocusPreserved {
		t.Fatalf("status semantics or live-update focus stability failed: %+v", report)
	}
	if report.CompactHeight < 175 || report.CompactHeight > 185 || report.DefaultHeight <= report.CompactHeight || !report.Maximized || report.MaximizePressed != "true" {
		t.Fatalf("terminal compact/default/maximize modes failed: %+v", report)
	}
	if !strings.Contains(report.TerminalSession, "LOGS") || !strings.Contains(report.TerminalOutput, "service ready") || !report.DisconnectIsVisible || !report.TerminalModeLogs {
		t.Fatalf("container logs terminal failed: %+v", report)
	}
	if report.CollapsedHeight != 0 || !report.InvokerFocusRestored {
		t.Fatalf("terminal collapse did not restore an unobscured invoker focus: %+v", report)
	}
}

type offlineReport struct {
	Offline            bool   `json:"offline"`
	SystemTitle        string `json:"systemTitle"`
	BannerVisible      bool   `json:"bannerVisible"`
	BannerMessage      string `json:"bannerMessage"`
	ServiceSkeletons   int    `json:"serviceSkeletons"`
	ContainerSkeletons int    `json:"containerSkeletons"`
	HostShellHidden    bool   `json:"hostShellHidden"`
}

func TestOfflineDashboardKeepsTruthfulPlaceholders(t *testing.T) {
	chrome := chromePath(t)
	assets, err := homelab.Static()
	if err != nil {
		t.Fatal(err)
	}
	static, err := httpapi.NewStaticHandler(assets)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(static)
	defer server.Close()

	allocatorOptions := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chrome), chromedp.Flag("no-sandbox", true), chromedp.Flag("disable-gpu", true))
	allocator, cancelAllocator := chromedp.NewExecAllocator(context.Background(), allocatorOptions...)
	defer cancelAllocator()
	ctx, cancel := chromedp.NewContext(allocator)
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, 15*time.Second)
	defer timeout()

	var report offlineReport
	err = chromedp.Run(ctx,
		chromedp.EmulateViewport(1024, 768),
		chromedp.Navigate(server.URL+"/"),
		chromedp.WaitVisible("#dashboard", chromedp.ByQuery),
		chromedp.Sleep(1200*time.Millisecond),
		chromedp.Evaluate(`(() => ({
          offline: document.body.classList.contains('is-offline'),
          systemTitle: document.querySelector('#system-title').textContent,
          bannerVisible: !document.querySelector('#offline-banner').hidden,
          bannerMessage: document.querySelector('#offline-message').textContent,
          serviceSkeletons: document.querySelectorAll('.service-card.skeleton-card').length,
          containerSkeletons: document.querySelectorAll('.container-item.skeleton-container').length,
          hostShellHidden: document.querySelector('#terminal-host-shell').hidden
        }))()`, &report),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Offline || report.SystemTitle != "Unable to reach server" || !report.BannerVisible || !strings.Contains(report.BannerMessage, "Unable to reach") {
		t.Fatalf("offline state is not explicit: %+v", report)
	}
	if report.ServiceSkeletons < 2 || report.ContainerSkeletons < 2 {
		t.Fatalf("offline placeholders were replaced with fake data: %+v", report)
	}
	if !report.HostShellHidden {
		t.Fatalf("host shell was exposed without an explicit capability: %+v", report)
	}
}

type hostShellReport struct {
	ButtonVisible     bool   `json:"buttonVisible"`
	InitialLabel      string `json:"initialLabel"`
	InitialHostOutput bool   `json:"initialHostOutput"`
	CancelledLabel    string `json:"cancelledLabel"`
	ActiveLabel       string `json:"activeLabel"`
	ActiveOutput      string `json:"activeOutput"`
	ActiveModeHost    bool   `json:"activeModeHost"`
	ActiveConnected   bool   `json:"activeConnected"`
	ActiveExpanded    bool   `json:"activeExpanded"`
	DisconnectVisible bool   `json:"disconnectVisible"`
	DisconnectedLabel string `json:"disconnectedLabel"`
	UnavailableLabel  string `json:"unavailableLabel"`
	LostLabel         string `json:"lostLabel"`
	LostStillLabel    string `json:"lostStillLabel"`
	LostButtonHidden  bool   `json:"lostButtonHidden"`
}

func TestDemoHostShellIsExplicitAndNeverSilentlyReopened(t *testing.T) {
	chrome := chromePath(t)
	assets, err := homelab.Static()
	if err != nil {
		t.Fatal(err)
	}
	static, err := httpapi.NewStaticHandler(assets)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(static)
	defer server.Close()
	serverURL, _ := url.Parse(server.URL)

	allocatorOptions := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chrome), chromedp.Flag("no-sandbox", true), chromedp.Flag("disable-gpu", true))
	allocator, cancelAllocator := chromedp.NewExecAllocator(context.Background(), allocatorOptions...)
	defer cancelAllocator()
	ctx, cancel := chromedp.NewContext(allocator)
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, 25*time.Second)
	defer timeout()

	var failures []string
	var mu sync.Mutex
	chromedp.ListenTarget(ctx, func(event any) {
		mu.Lock()
		defer mu.Unlock()
		switch value := event.(type) {
		case *cdpruntime.EventExceptionThrown:
			failures = append(failures, value.ExceptionDetails.Text)
		case *cdpruntime.EventConsoleAPICalled:
			if value.Type == cdpruntime.APITypeError {
				failures = append(failures, "console.error")
			}
		case *network.EventRequestWillBeSent:
			requested, parseErr := url.Parse(value.Request.URL)
			if parseErr == nil && (requested.Scheme == "http" || requested.Scheme == "https" || requested.Scheme == "ws" || requested.Scheme == "wss") && requested.Host != serverURL.Host {
				failures = append(failures, "external request: "+value.Request.URL)
			}
		}
	})

	var report hostShellReport
	err = chromedp.Run(ctx,
		network.Enable(),
		chromedp.EmulateViewport(1440, 900),
		chromedp.Navigate(server.URL+"/?demo=1"),
		chromedp.WaitVisible("#terminal-host-shell", chromedp.ByQuery),
		chromedp.WaitVisible(".xterm", chromedp.ByQuery),
		chromedp.Evaluate(`(() => ({
          buttonVisible: !document.querySelector('#terminal-host-shell').hidden,
          initialLabel: document.querySelector('#terminal-session-label').textContent,
          initialHostOutput: (document.querySelector('.xterm-rows')?.textContent || '').includes('binhminh@debian-server')
        }))()`, &report),
		chromedp.Click("#terminal-host-shell", chromedp.ByQuery),
		chromedp.WaitVisible("#host-shell-dialog", chromedp.ByQuery),
		chromedp.Click(`#host-shell-confirm-form button[value="cancel"]`, chromedp.ByQuery),
		chromedp.Poll(`!document.querySelector('#host-shell-dialog').open`, nil, chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Evaluate(`document.querySelector('#terminal-session-label').textContent`, &report.CancelledLabel),
		chromedp.Click("#terminal-host-shell", chromedp.ByQuery),
		chromedp.WaitVisible("#host-shell-dialog", chromedp.ByQuery),
		chromedp.Click(`#host-shell-confirm-form button[value="confirm"]`, chromedp.ByQuery),
		chromedp.WaitVisible("#terminal-disconnect", chromedp.ByQuery),
		chromedp.Sleep(150*time.Millisecond),
		chromedp.Evaluate(`(() => ({
          activeLabel: document.querySelector('#terminal-session-label').textContent,
          activeOutput: document.querySelector('.xterm-rows')?.textContent || '',
          activeModeHost: document.querySelector('#terminal-panel').classList.contains('terminal-mode-host'),
          activeConnected: document.querySelector('#terminal-panel').classList.contains('terminal-state-connected'),
          activeExpanded: document.querySelector('#terminal-toggle').getAttribute('aria-expanded') === 'true',
          disconnectVisible: !document.querySelector('#terminal-disconnect').hidden
        }))()`, &report),
		chromedp.Click("#terminal-disconnect", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('#terminal-session-label').textContent`, &report.DisconnectedLabel),
		chromedp.Navigate(server.URL+"/?demo=1&hostAgent=offline"),
		chromedp.WaitVisible("#terminal-host-shell", chromedp.ByQuery),
		chromedp.Click("#terminal-host-shell", chromedp.ByQuery),
		chromedp.WaitVisible("#host-shell-dialog", chromedp.ByQuery),
		chromedp.Click(`#host-shell-confirm-form button[value="confirm"]`, chromedp.ByQuery),
		chromedp.WaitVisible("#toast-region .toast", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('#terminal-session-label').textContent`, &report.UnavailableLabel),
		chromedp.Navigate(server.URL+"/?demo=1&hostAgent=disconnect"),
		chromedp.WaitVisible("#terminal-host-shell", chromedp.ByQuery),
		chromedp.Click("#terminal-host-shell", chromedp.ByQuery),
		chromedp.WaitVisible("#host-shell-dialog", chromedp.ByQuery),
		chromedp.Click(`#host-shell-confirm-form button[value="confirm"]`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('#terminal-session-label').textContent === 'HOST · DISCONNECTED'`, nil, chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Evaluate(`document.querySelector('#terminal-session-label').textContent`, &report.LostLabel),
		chromedp.Sleep(3200*time.Millisecond),
		chromedp.Evaluate(`(() => ({
          lostStillLabel: document.querySelector('#terminal-session-label').textContent,
          lostButtonHidden: document.querySelector('#terminal-disconnect').hidden
		}))()`, &report),
	)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(failures) > 0 {
		t.Fatalf("browser failures: %s", strings.Join(failures, "; "))
	}
	if !report.ButtonVisible || report.InitialLabel != "IDLE" || report.InitialHostOutput {
		t.Fatalf("host shell opened without an explicit action: %+v", report)
	}
	if report.CancelledLabel != "IDLE" {
		t.Fatalf("declining confirmation changed the terminal session: %+v", report)
	}
	if !strings.Contains(report.ActiveLabel, "HOST") || !strings.Contains(report.ActiveLabel, "debian-server") || !strings.Contains(report.ActiveLabel, "binhminh") || !strings.Contains(report.ActiveOutput, "binhminh@debian-server") || !report.DisconnectVisible || !report.ActiveModeHost || !report.ActiveConnected || !report.ActiveExpanded {
		t.Fatalf("explicit host shell did not render the host identity: %+v", report)
	}
	if report.DisconnectedLabel != "IDLE" {
		t.Fatalf("explicit disconnect did not close the host shell: %+v", report)
	}
	if report.UnavailableLabel != "HOST · UNAVAILABLE" {
		t.Fatalf("offline host agent state was not explicit: %+v", report)
	}
	if report.LostLabel != "HOST · DISCONNECTED" || report.LostStillLabel != "HOST · DISCONNECTED" || !report.LostButtonHidden {
		t.Fatalf("lost host shell was silently reopened: %+v", report)
	}
}

func chromePath(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{"google-chrome", "chromium", "chromium-browser"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	t.Skip("Chrome/Chromium is not installed")
	return ""
}

func near(left, right, tolerance float64) bool {
	difference := left - right
	if difference < 0 {
		difference = -difference
	}
	return difference <= tolerance
}

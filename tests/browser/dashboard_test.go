//go:build browser

package browser_test

import (
	"context"
	"io"
	"net/http"
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
	ScrollWidth           float64  `json:"scrollWidth"`
	ViewportWidth         float64  `json:"viewportWidth"`
	ActivePanels          int      `json:"activePanels"`
	ActiveWorkspace       string   `json:"activeWorkspace"`
	TerminalHeight        float64  `json:"terminalHeight"`
	ToolbarHeight         float64  `json:"toolbarHeight"`
	TerminalBody          float64  `json:"terminalBody"`
	Collapsed             bool     `json:"collapsed"`
	ToggleExpanded        string   `json:"toggleExpanded"`
	HasXterm              bool     `json:"hasXterm"`
	Overflow              []string `json:"overflow"`
	TouchFailures         []string `json:"touchFailures"`
	A11yFailures          []string `json:"a11yFailures"`
	OverviewOuterColumns  int      `json:"overviewOuterColumns"`
	OverviewLayoutColumns int      `json:"overviewLayoutColumns"`
	SystemMetricColumns   int      `json:"systemMetricColumns"`
	SystemSparklines      int      `json:"systemSparklines"`
	BrandTruncated        bool     `json:"brandTruncated"`
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
		{"phone-375", 375, 812},
		{"phone-414", 414, 896},
		{"workbench-768", 768, 900},
		{"workbench-max-899", 899, 900},
		{"tablet-min-900", 900, 900},
		{"desktop-min-1280", 1280, 900},
		{"wide-2048", 2048, 1080},
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
				chromedp.WaitVisible("#workspace-overview", chromedp.ByQuery),
				chromedp.WaitReady(".xterm", chromedp.ByQuery),
				chromedp.Evaluate(`(() => {
          const rect = (selector) => document.querySelector(selector).getBoundingClientRect();
          const gridColumns = (selector) => {
            const value = getComputedStyle(document.querySelector(selector)).gridTemplateColumns.trim();
            return value && value !== 'none' ? value.split(/\s+/).length : 0;
          };
          const isVisibleInViewport = (node) => {
            const style = getComputedStyle(node);
            const bounds = node.getBoundingClientRect();
            return style.display !== 'none' && style.visibility !== 'hidden' && bounds.width > 0
              && bounds.right > 0 && bounds.left < window.innerWidth;
          };
          const activePanels = [...document.querySelectorAll('[data-workspace-panel]:not([hidden])')];
          return {
            scrollWidth: document.documentElement.scrollWidth,
            viewportWidth: window.innerWidth,
            activePanels: activePanels.length,
            activeWorkspace: activePanels[0]?.dataset.workspacePanel || '',
            overviewOuterColumns: gridColumns('#workspace-overview'),
            overviewLayoutColumns: gridColumns('.overview-layout'),
            systemMetricColumns: gridColumns('#system-card'),
            systemSparklines: [...document.querySelectorAll('#system-card .sparkline')]
              .filter((node) => getComputedStyle(node).display !== 'none').length,
			brandTruncated: (() => {
			  const brand = document.querySelector('.brand-name');
			  return brand.scrollWidth > brand.clientWidth + 1;
			})(),
            terminalHeight: rect('#terminal-panel').height,
            toolbarHeight: rect('#terminal-panel .terminal-toolbar').height,
            terminalBody: rect('#terminal-body').height,
            collapsed: document.querySelector('#terminal-panel').classList.contains('is-collapsed'),
            toggleExpanded: document.querySelector('#terminal-toggle').getAttribute('aria-expanded'),
            hasXterm: Boolean(document.querySelector('.xterm .xterm-helper-textarea')),
            overflow: [...document.querySelectorAll('body *')]
              .filter((node) => {
                if (node.closest('.xterm')) return false;
                const bounds = node.getBoundingClientRect();
                return isVisibleInViewport(node)
                  && (bounds.right > window.innerWidth + 1 || bounds.left < -1);
              })
              .slice(0, 10)
              .map((node) => node.tagName.toLowerCase() + '#' + node.id + '.' + String(node.className)),
            touchFailures: [...document.querySelectorAll('#sidebar-open, #sidebar-collapse, #terminal-toggle, .icon-button, #overview-alerts-open, #overview-trend-refresh')]
              .filter((node) => {
                const bounds = node.getBoundingClientRect();
                return isVisibleInViewport(node)
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
			if viewport.width == 375 {
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
				if !mobileWorkbench.Active || mobileWorkbench.Collapsed || mobileWorkbench.Top > 57 || mobileWorkbench.Right < 374 || mobileWorkbench.Bottom < 811 || mobileWorkbench.Body < 650 {
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
			if report.ActivePanels != 1 || report.ActiveWorkspace != "overview" || !report.HasXterm {
				t.Fatalf("cold load did not isolate the Overview workspace or initialize xterm: %+v", report)
			}
			if report.OverviewOuterColumns != 1 {
				t.Fatalf("overview outer workspace must always occupy one grid column: %+v", report)
			}
			if viewport.width >= 900 && report.OverviewLayoutColumns != 2 {
				t.Fatalf("overview must retain its two-pane workbench layout from 900px upward: %+v", report)
			}
			if viewport.width < 900 && report.OverviewLayoutColumns != 1 {
				t.Fatalf("overview must collapse to one content column below 900px: %+v", report)
			}
			if report.SystemSparklines != 0 {
				t.Fatalf("overview system snapshot must not duplicate the dedicated resource trend with sparklines: %+v", report)
			}
			if viewport.width >= 1280 && report.SystemMetricColumns != 3 {
				t.Fatalf("wide overview system snapshot must use the compact three-column metric grid: %+v", report)
			}
			if viewport.width >= 560 && viewport.width < 900 && report.SystemMetricColumns != 3 {
				t.Fatalf("tablet overview system snapshot must use the compact three-column metric grid: %+v", report)
			}
			if viewport.width >= 600 && report.BrandTruncated {
				t.Fatalf("tablet and desktop headers must retain the full dashboard name: %+v", report)
			}
			if !report.Collapsed || report.ToggleExpanded != "false" || report.TerminalBody != 0 || report.TerminalHeight < 40 || report.TerminalHeight > 52 || report.ToolbarHeight < 40 || report.ToolbarHeight > 52 {
				t.Fatalf("cold terminal must be a collapsed 44px-ish status bar: %+v", report)
			}
			if viewport.width < 900 {
				if len(report.TouchFailures) > 0 {
					t.Fatalf("sub-900 touch targets are smaller than 44px: %+v", report)
				}
			}
		})
	}
}

func TestDemoWorkspaceNavigationAndPersistence(t *testing.T) {
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
	ctx, timeout := context.WithTimeout(ctx, 30*time.Second)
	defer timeout()

	err = chromedp.Run(ctx,
		chromedp.EmulateViewport(1440, 900),
		chromedp.Navigate(server.URL+"/?demo=1"),
		chromedp.WaitVisible("#workspace-overview", chromedp.ByQuery),
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, workspace := range []string{"services", "containers", "history", "alerts", "overview"} {
		workspace := workspace
		selector := `[data-workspace="` + workspace + `"]`
		panelSelector := `#workspace-` + workspace
		titleSelector := `#` + workspace + `-title`
		if workspace == "overview" {
			titleSelector = "#health-overview"
		}
		err = chromedp.Run(ctx,
			chromedp.Click(selector, chromedp.ByQuery),
			chromedp.WaitVisible(panelSelector, chromedp.ByQuery),
			chromedp.Poll(`(() => {
          const active = [...document.querySelectorAll('[data-workspace-panel]:not([hidden])')];
          const button = document.querySelector('`+selector+`');
          return active.length === 1 && active[0].id === '`+panelSelector[1:]+`'
            && button?.getAttribute('aria-current') === 'page'
            && document.activeElement === document.querySelector('`+titleSelector+`');
        })()`, nil, chromedp.WithPollingInterval(25*time.Millisecond), chromedp.WithPollingTimeout(2*time.Second)),
		)
		if err != nil {
			t.Fatalf("workspace %s navigation failed: %v", workspace, err)
		}
		if workspace == "services" {
			err = chromedp.Run(ctx, chromedp.WaitVisible(".service-card:not(.skeleton-card)", chromedp.ByQuery))
			if err != nil {
				t.Fatalf("services did not render after workspace activation: %v", err)
			}
		}
		if workspace == "containers" {
			err = chromedp.Run(ctx, chromedp.WaitVisible(".container-item:not(.skeleton-container)", chromedp.ByQuery))
			if err != nil {
				t.Fatalf("containers did not render after workspace activation: %v", err)
			}
		}
		if workspace == "history" {
			err = chromedp.Run(ctx, chromedp.Poll(`(() => {
            const canvas = document.querySelector('#history-chart');
            const chart = window.Chart?.getChart(canvas);
            return canvas?.getBoundingClientRect().width > 100 && (chart?.data?.datasets?.[0]?.data?.length || 0) > 0;
          })()`, nil, chromedp.WithPollingTimeout(3*time.Second)))
			if err != nil {
				t.Fatalf("history chart was not resized after workspace activation: %v", err)
			}
		}
	}

	err = chromedp.Run(ctx,
		chromedp.Click(`[data-sidebar-action="terminal"]`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('#terminal-toggle').getAttribute('aria-expanded') === 'true' && document.activeElement?.matches('#terminal .xterm-helper-textarea')`, nil,
			chromedp.WithPollingInterval(25*time.Millisecond), chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Click(`[data-workspace="history"]`, chromedp.ByQuery),
		chromedp.Click("#sidebar-collapse", chromedp.ByQuery),
		chromedp.Poll(`document.body.classList.contains('sidebar-collapsed')`, nil, chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Navigate(server.URL+"/?demo=1"),
		chromedp.WaitVisible("#workspace-history", chromedp.ByQuery),
	)
	if err != nil {
		t.Fatal(err)
	}
	var persisted struct {
		Collapsed   bool `json:"collapsed"`
		Active      bool `json:"active"`
		UnnamedNavs int  `json:"unnamedNavs"`
	}
	err = chromedp.Run(ctx, chromedp.Evaluate(`(() => ({
      collapsed: document.body.classList.contains('sidebar-collapsed'),
      active: !document.querySelector('#workspace-history').hidden,
      unnamedNavs: [...document.querySelectorAll('#workspace-sidebar [data-workspace], #workspace-sidebar [data-sidebar-action]')]
        .filter((button) => !button.getAttribute('aria-label'))
        .length
    }))()`, &persisted))
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.Collapsed || !persisted.Active || persisted.UnnamedNavs != 0 {
		t.Fatalf("workspace/sidebar preferences did not persist across reload: %+v", persisted)
	}
}

func TestDemoMobileSidebarDrawer(t *testing.T) {
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

	var report struct {
		DrawerClosedInert bool     `json:"drawerClosedInert"`
		DrawerOpened      bool     `json:"drawerOpened"`
		DrawerModal       bool     `json:"drawerModal"`
		BackgroundInert   bool     `json:"backgroundInert"`
		FocusInSidebar    bool     `json:"focusInSidebar"`
		TouchFailures     []string `json:"touchFailures"`
		TrapWorks         bool     `json:"trapWorks"`
		EscapeRestored    bool     `json:"escapeRestored"`
		BackdropRestored  bool     `json:"backdropRestored"`
		ServicesActive    bool     `json:"servicesActive"`
		ServicesFocused   bool     `json:"servicesFocused"`
	}
	err = chromedp.Run(ctx,
		chromedp.EmulateViewport(375, 812),
		chromedp.Navigate(server.URL+"/?demo=1"),
		chromedp.WaitVisible("#workspace-overview", chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
          const sidebar = document.querySelector('#workspace-sidebar');
          return sidebar.inert && sidebar.getAttribute('aria-hidden') === 'true';
        })()`, &report.DrawerClosedInert),
		chromedp.Click("#sidebar-open", chromedp.ByQuery),
		chromedp.Poll(`document.body.classList.contains('sidebar-drawer-open') && !document.querySelector('#sidebar-backdrop').hidden && document.querySelector('#sidebar-open').getAttribute('aria-expanded') === 'true'`, nil,
			chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Evaluate(`(() => {
          const sidebar = document.querySelector('#workspace-sidebar');
          const visible = (node) => {
            const style = getComputedStyle(node);
            const rect = node.getBoundingClientRect();
            return style.display !== 'none' && style.visibility !== 'hidden' && rect.width > 0 && rect.right > 0 && rect.left < innerWidth;
          };
          return {
            drawerOpened: document.body.classList.contains('sidebar-drawer-open') && !document.querySelector('#sidebar-backdrop').hidden,
            drawerModal: sidebar.getAttribute('role') === 'dialog' && sidebar.getAttribute('aria-modal') === 'true',
            backgroundInert: document.querySelector('.app-header').inert && document.querySelector('#dashboard').inert && document.querySelector('#terminal-panel').inert,
            focusInSidebar: Boolean(document.activeElement?.closest('#workspace-sidebar')),
            touchFailures: [...document.querySelectorAll('#sidebar-open, #sidebar-collapse, #workspace-sidebar [data-workspace], #workspace-sidebar [data-sidebar-action]')]
              .filter((node) => visible(node) && (node.getBoundingClientRect().width < 44 || node.getBoundingClientRect().height < 44))
              .map((node) => node.id || node.dataset.workspace || node.dataset.sidebarAction || node.className)
          };
        })()`, &report),
		chromedp.Focus(`[data-sidebar-action="terminal"]`, chromedp.ByQuery),
		chromedp.KeyEvent(kb.Tab),
		chromedp.Evaluate(`(() => {
          const sidebar = document.querySelector('#workspace-sidebar');
          const first = [...sidebar.querySelectorAll("button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex='-1'])")]
            .find((element) => !element.hidden && element.offsetParent !== null);
          return document.activeElement === first;
        })()`, &report.TrapWorks),
		chromedp.KeyEvent(kb.Escape),
		chromedp.Poll(`!document.body.classList.contains('sidebar-drawer-open') && document.activeElement === document.querySelector('#sidebar-open')`, nil,
			chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Evaluate(`!document.body.classList.contains('sidebar-drawer-open') && document.activeElement === document.querySelector('#sidebar-open')`, &report.EscapeRestored),
		chromedp.Click("#sidebar-open", chromedp.ByQuery),
		chromedp.Click("#sidebar-backdrop", chromedp.ByQuery),
		chromedp.Poll(`!document.body.classList.contains('sidebar-drawer-open') && document.activeElement === document.querySelector('#sidebar-open')`, nil,
			chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Evaluate(`!document.body.classList.contains('sidebar-drawer-open') && document.activeElement === document.querySelector('#sidebar-open')`, &report.BackdropRestored),
		chromedp.Click("#sidebar-open", chromedp.ByQuery),
		chromedp.Focus(`[data-workspace="services"]`, chromedp.ByQuery),
		chromedp.KeyEvent(kb.Enter),
		chromedp.Poll(`!document.querySelector('#workspace-services').hidden && !document.body.classList.contains('sidebar-drawer-open') && document.activeElement === document.querySelector('#services-title')`, nil,
			chromedp.WithPollingInterval(25*time.Millisecond), chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Evaluate(`(() => ({
          servicesActive: !document.querySelector('#workspace-services').hidden,
          servicesFocused: document.activeElement === document.querySelector('#services-title')
        }))()`, &report),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !report.DrawerClosedInert || !report.DrawerOpened || !report.DrawerModal || !report.BackgroundInert || !report.FocusInSidebar || !report.TrapWorks || !report.EscapeRestored || !report.BackdropRestored || !report.ServicesActive || !report.ServicesFocused || len(report.TouchFailures) > 0 {
		t.Fatalf("mobile workspace drawer regression: %+v", report)
	}
}

func TestDemoEdgeAlertsAndHistoryDoNotOverflow(t *testing.T) {
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

	viewports := []struct {
		name          string
		width, height int64
	}{
		{"phone-375", 375, 812},
		{"tablet-900", 900, 900},
		{"desktop-1440", 1440, 900},
		{"wide-2048", 2048, 1080},
	}
	for _, viewport := range viewports {
		viewport := viewport
		t.Run(viewport.name, func(t *testing.T) {
			ctx, cancel := chromedp.NewContext(allocator)
			defer cancel()
			ctx, timeout := context.WithTimeout(ctx, 30*time.Second)
			defer timeout()
			var report struct {
				Count             int  `json:"count"`
				EmptyHidden       bool `json:"emptyHidden"`
				PartialVisible    bool `json:"partialVisible"`
				ListScrollable    bool `json:"listScrollable"`
				SourceCompacted   bool `json:"sourceCompacted"`
				FallbackCompacted bool `json:"fallbackCompacted"`
				RawSourceKept     bool `json:"rawSourceKept"`
				AlertBounds       bool `json:"alertBounds"`
				FooterBounds      bool `json:"footerBounds"`
				RefreshTouch      bool `json:"refreshTouch"`
				ChartReady        bool `json:"chartReady"`
				PageOverflow      bool `json:"pageOverflow"`
			}
			err := chromedp.Run(ctx,
				chromedp.EmulateViewport(viewport.width, viewport.height),
				chromedp.Navigate(server.URL+"/?demo=1&edge=1"),
				chromedp.Evaluate(`document.querySelector('[data-workspace="alerts"]').click()`, nil),
				chromedp.WaitVisible(".alert-item", chromedp.ByQuery),
				chromedp.Evaluate(`(() => {
          const within = (child, parent) => {
            const c = child.getBoundingClientRect();
            const p = parent.getBoundingClientRect();
            return c.left >= p.left - 1 && c.right <= p.right + 1 && c.top >= p.top - 1 && c.bottom <= p.bottom + 1;
          };
          const list = document.querySelector('#alerts-list');
          const items = [...document.querySelectorAll('.alert-item')];
          const raw = 'local/container/04fe5d1ce9fc995c2f071051aaff95fb94a0fcdc39cfc54cc5cea05d5678edfc';
          return {
            count: items.length,
            emptyHidden: document.querySelector('#alerts-empty').hidden,
            partialVisible: !document.querySelector('#alerts-partial').hidden,
            listScrollable: list.scrollHeight > list.clientHeight,
            sourceCompacted: items.some((item) => item.querySelector('.alert-meta')?.textContent.includes('LOCAL · CONTAINER · immich_server')),
            fallbackCompacted: items.some((item) => item.querySelector('.alert-meta')?.textContent.includes('8d439abf2f34…7b0a91')),
            rawSourceKept: items.some((item) => item.querySelector('.alert-meta')?.title === raw && item.querySelector('.alert-meta')?.getAttribute('aria-label') === 'Alert source: ' + raw),
            alertBounds: items.every((item) => {
              const content = item.querySelector('.alert-content');
              const meta = item.querySelector('.alert-meta');
              return content && meta && within(content, item) && within(meta, item) && meta.scrollWidth <= meta.clientWidth + 1;
            }),
            pageOverflow: document.documentElement.scrollWidth > innerWidth + 1
          };
        })()`, &report),
				chromedp.Evaluate(`document.querySelector('[data-workspace="history"]').click()`, nil),
				chromedp.WaitVisible("#history-panel", chromedp.ByQuery),
				chromedp.Click(`[data-history-kind="container"]`, chromedp.ByQuery),
				chromedp.Poll(`document.querySelector('#history-resource').options.length > 0 && document.querySelector('#history-empty').hidden`, nil,
					chromedp.WithPollingTimeout(3*time.Second)),
				chromedp.Evaluate(`(() => {
          const select = document.querySelector('#history-resource');
          const option = [...select.options].find((item) => item.textContent.includes('transcoding_machine_learning_worker'));
          if (!option) return false;
          select.value = option.value;
          select.dispatchEvent(new Event('change', { bubbles: true }));
          return true;
        })()`, nil),
				chromedp.Poll(`document.querySelector('#history-resource-summary').textContent.includes('transcoding_machine_learning_worker') && document.querySelector('#history-empty').hidden`, nil,
					chromedp.WithPollingTimeout(3*time.Second)),
				chromedp.Evaluate(`(() => {
          const body = document.querySelector('.history-body');
          const footer = document.querySelector('.history-footer');
          const button = document.querySelector('#history-refresh');
          const within = (child, parent) => {
            const c = child.getBoundingClientRect();
            const p = parent.getBoundingClientRect();
            return c.left >= p.left - 1 && c.right <= p.right + 1 && c.top >= p.top - 1 && c.bottom <= p.bottom + 1;
          };
          const canvas = document.querySelector('#history-chart');
          const chart = window.Chart?.getChart(canvas);
          return {
            footerBounds: [...footer.children].every((item) => within(item, body)) && within(button, body),
            refreshTouch: innerWidth >= 900 || (button.getBoundingClientRect().width >= 44 && button.getBoundingClientRect().height >= 44),
            chartReady: canvas.getBoundingClientRect().width > 100 && (chart?.data?.datasets?.[0]?.data?.length || 0) > 0,
            pageOverflow: document.documentElement.scrollWidth > innerWidth + 1
          };
        })()`, &report),
			)
			if err != nil {
				t.Fatal(err)
			}
			if report.Count < 50 || !report.EmptyHidden || !report.PartialVisible || !report.ListScrollable || !report.SourceCompacted || !report.FallbackCompacted || !report.RawSourceKept || !report.AlertBounds || !report.FooterBounds || !report.RefreshTouch || !report.ChartReady || report.PageOverflow {
				t.Fatalf("edge alert/history overflow regression: %+v", report)
			}
		})
	}
}

func TestDemoOverviewTriageActionsAndTrend(t *testing.T) {
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
	ctx, timeout := context.WithTimeout(ctx, 30*time.Second)
	defer timeout()

	var report struct {
		Brand               string `json:"brand"`
		HasRocket           bool   `json:"hasRocket"`
		AttentionTotal      int    `json:"attentionTotal"`
		AttentionVisible    int    `json:"attentionVisible"`
		FirstIsCritical     bool   `json:"firstIsCritical"`
		HasLogsAction       bool   `json:"hasLogsAction"`
		PulseRows           int    `json:"pulseRows"`
		TrendPoints         int    `json:"trendPoints"`
		TrendSeries         int    `json:"trendSeries"`
		AttentionIsQuiet    bool   `json:"attentionIsQuiet"`
		SourceCompacted     bool   `json:"sourceCompacted"`
		AlertWorkspaceOpen  bool   `json:"alertWorkspaceOpen"`
		HealthMatchesTriage bool   `json:"healthMatchesTriage"`
		NestedServiceHealth bool   `json:"nestedServiceHealth"`
	}
	err = chromedp.Run(ctx,
		chromedp.EmulateViewport(1440, 900),
		chromedp.Navigate(server.URL+"/?demo=1&edge=1"),
		chromedp.WaitVisible("#overview-attention-list .overview-action-item", chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
          import('/js/overview.js').then(({ collectOverviewIncidents }) => {
            window.__nestedServiceHealthIncident = collectOverviewIncidents({
              services: [{ name: 'Nested service fixture', health: { status: 'down' } }],
            }).some((incident) => incident.kind === 'service' && incident.title === 'Nested service fixture is down');
          });
        })()`, nil),
		chromedp.Poll(`typeof window.__nestedServiceHealthIncident === 'boolean'`, nil,
			chromedp.WithPollingInterval(25*time.Millisecond), chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Poll(`(() => {
          const chart = window.Chart?.getChart(document.querySelector('#overview-trend-chart'));
          return (chart?.data?.datasets?.length || 0) === 3 && (chart?.data?.datasets?.[0]?.data?.length || 0) > 0;
        })()`, nil, chromedp.WithPollingInterval(25*time.Millisecond), chromedp.WithPollingTimeout(3*time.Second)),
		chromedp.Evaluate(`(() => {
          const attention = [...document.querySelectorAll('#overview-attention-list .overview-action-item')];
          const chart = window.Chart?.getChart(document.querySelector('#overview-trend-chart'));
          return {
            brand: document.querySelector('.brand-name')?.textContent.trim() || '',
            hasRocket: Boolean(document.querySelector('.brand-mark')),
            attentionTotal: Number(document.querySelector('#overview-attention-count')?.textContent || 0),
            attentionVisible: attention.length,
            firstIsCritical: attention[0]?.dataset.level === 'critical',
            hasLogsAction: attention.some((item) => [...item.querySelectorAll('button')].some((button) => button.textContent.trim() === 'LOGS')),
            pulseRows: document.querySelectorAll('#overview-service-pulse-list .overview-action-item').length,
            trendPoints: chart?.data?.datasets?.[0]?.data?.length || 0,
            trendSeries: chart?.data?.datasets?.length || 0,
            attentionIsQuiet: !document.querySelector('#overview-attention-list').hasAttribute('aria-live'),
            sourceCompacted: attention.some((item) => {
              const detail = item.querySelector('.overview-action-detail');
              return detail?.title?.includes('8d439abf2f349e6c2d35f0df8c139738571c3ba6d7eefcb732c8ee86f67b0a91')
                && !detail.textContent.includes('8d439abf2f349e6c2d35f0df8c139738571c3ba6d7eefcb732c8ee86f67b0a91');
            }),
            healthMatchesTriage: (() => {
              const count = Number(document.querySelector('#overview-attention-count')?.textContent || 0);
              const health = document.querySelector('#overview-health')?.textContent.trim() || '';
              const detail = document.querySelector('#overview-health-detail')?.textContent.trim() || '';
              return health !== 'ACTION NEEDED' || detail.startsWith(String(count) + ' monitored');
            })(),
            nestedServiceHealth: window.__nestedServiceHealthIncident === true,
          };
        })()`, &report),
		chromedp.Poll(`(() => [...document.querySelectorAll('#overview-attention-list button')]
          .some((button) => button.textContent.trim() === 'LOGS'))()`, nil,
			chromedp.WithPollingInterval(25*time.Millisecond), chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Evaluate(`([...document.querySelectorAll('#overview-attention-list button')]
          .find((button) => button.textContent.trim() === 'LOGS'))?.click()`, nil),
		chromedp.WaitVisible("#log-viewer", chromedp.ByQuery),
		chromedp.Click("#overview-alerts-open", chromedp.ByQuery),
		chromedp.WaitVisible("#workspace-alerts", chromedp.ByQuery),
		chromedp.Evaluate(`!document.querySelector('#workspace-alerts').hidden`, &report.AlertWorkspaceOpen),
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Brand != "HOMELAB DASHBOARD" || report.HasRocket || report.AttentionTotal < 50 || report.AttentionVisible == 0 || report.AttentionVisible > 5 || !report.FirstIsCritical || !report.HasLogsAction || report.PulseRows == 0 || report.TrendPoints == 0 || report.TrendSeries != 3 || !report.AttentionIsQuiet || !report.SourceCompacted || !report.AlertWorkspaceOpen || !report.HealthMatchesTriage || !report.NestedServiceHealth {
		t.Fatalf("overview triage/trend regression: %+v", report)
	}

	mobileCtx, cancelMobile := chromedp.NewContext(allocator)
	defer cancelMobile()
	mobileCtx, mobileTimeout := context.WithTimeout(mobileCtx, 25*time.Second)
	defer mobileTimeout()
	var mobile struct {
		Overflow      bool `json:"overflow"`
		TouchFailures int  `json:"touchFailures"`
	}
	err = chromedp.Run(mobileCtx,
		chromedp.EmulateViewport(375, 812),
		chromedp.Navigate(server.URL+"/?demo=1&edge=1"),
		chromedp.WaitVisible("#overview-attention-list .overview-action-item", chromedp.ByQuery),
		chromedp.Evaluate(`(() => ({
          overflow: document.documentElement.scrollWidth > innerWidth + 1,
          touchFailures: [...document.querySelectorAll('#overview-attention .text-button, #overview-trend-refresh')]
            .filter((button) => {
              const rect = button.getBoundingClientRect();
              const style = getComputedStyle(button);
              return style.display !== 'none' && rect.width > 0 && rect.height > 0
                && rect.left < innerWidth && rect.right > 0 && (rect.width < 44 || rect.height < 44);
            }).length
        }))()`, &mobile),
	)
	if err != nil {
		t.Fatal(err)
	}
	if mobile.Overflow || mobile.TouchFailures != 0 {
		t.Fatalf("mobile overview triage regression: %+v", mobile)
	}
}

func TestGraphiteAmberThemeDoesNotRestoreCyanGlass(t *testing.T) {
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

	for _, path := range []string{"/css/style.css", "/css/tokens.css", "/js/history.js", "/js/metrics.js", "/js/overview.js", "/js/terminal.js", "/"} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("load %s: %v", path, err)
		}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("load %s returned %s", path, response.Status)
		}
		content := strings.ToLower(string(body))
		for _, forbidden := range []string{"#6ee2ff", "--cyan", "cyan-glow", "backdrop-filter"} {
			if strings.Contains(content, forbidden) {
				t.Fatalf("%s still contains removed cyan/glass token %q", path, forbidden)
			}
		}
		if path == "/" && (!strings.Contains(string(body), "HOMELAB DASHBOARD") || strings.Contains(content, "brand-mark")) {
			t.Fatalf("%s does not expose the text-only Homelab brand", path)
		}
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
	EditEndpoint         string  `json:"editEndpoint"`
	EditIcon             string  `json:"editIcon"`
	EditEmptyIcon        string  `json:"editEmptyIcon"`
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
	LogViewerVisible     bool    `json:"logViewerVisible"`
	XtermHidden          bool    `json:"xtermHidden"`
	LogSearchMatches     int     `json:"logSearchMatches"`
	LogPausePressed      string  `json:"logPausePressed"`
	LogFollowPressed     string  `json:"logFollowPressed"`
	LogDownloadName      string  `json:"logDownloadName"`
	LogDownloadBytes     int     `json:"logDownloadBytes"`
	InvokerFocusRestored bool    `json:"invokerFocusRestored"`
	DisconnectIsVisible  bool    `json:"disconnectIsVisible"`
	PartialBadgeVisible  bool    `json:"partialBadgeVisible"`
	OverviewHealth       string  `json:"overviewHealth"`
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
	ctx, timeout := context.WithTimeout(ctx, 40*time.Second)
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
		chromedp.Click(`[data-workspace="services"]`, chromedp.ByQuery),
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
		chromedp.Poll(`!document.querySelector('#service-dialog').open && document.activeElement === document.querySelector('#focus-add-service')`, nil,
			chromedp.WithPollingInterval(25*time.Millisecond), chromedp.WithPollingTimeout(2*time.Second)),
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
		chromedp.Poll(`(() => {
          const trigger = document.querySelector('.service-card[data-service-id^="demo-"] .service-menu-button');
          if (!trigger || trigger.hidden || trigger.disabled || trigger.getClientRects().length === 0) return false;
          window.__serviceMenuTrigger = trigger;
          trigger.focus({ preventScroll: true });
          return document.activeElement === trigger;
		})()`, nil, chromedp.WithPollingInterval(25*time.Millisecond), chromedp.WithPollingTimeout(3*time.Second)),
		chromedp.SendKeys(`.service-card[data-service-id^="demo-"] .service-menu-button`, kb.Enter, chromedp.ByQuery),
		chromedp.Poll(`(() => {
          const menu = document.querySelector('#context-menu');
          const trigger = document.querySelector('.service-card[data-service-id^="demo-"] .service-menu-button');
          return !menu.hidden && trigger.getAttribute('aria-expanded') === 'true' && document.activeElement === menu.querySelector('[role="menuitem"]');
        })()`, nil,
			chromedp.WithPollingInterval(25*time.Millisecond), chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Evaluate(`document.activeElement?.textContent || ''`, &report.MenuInitialItem),
		chromedp.SendKeys(`#context-menu [role="menuitem"]:first-child`, kb.ArrowDown, chromedp.ByQuery),
		chromedp.Poll(`document.activeElement === document.querySelector('#context-menu [role="menuitem"]:nth-child(2)')`, nil,
			chromedp.WithPollingInterval(25*time.Millisecond), chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Evaluate(`document.activeElement?.textContent || ''`, &report.MenuSecondItem),
		chromedp.SendKeys(`#context-menu [role="menuitem"]:nth-child(2)`, kb.Escape, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('#context-menu').hidden && document.activeElement === document.querySelector('.service-card[data-service-id^="demo-"] .service-menu-button')`, nil,
			chromedp.WithPollingInterval(25*time.Millisecond), chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Evaluate(`document.querySelector('#context-menu').hidden && document.activeElement === document.querySelector('.service-card[data-service-id^="demo-"] .service-menu-button')`, &report.MenuRestoredFocus),
		chromedp.Evaluate(`document.querySelector('.service-card[data-service-id="svc_immich"] .service-menu-button').click()`, nil),
		chromedp.WaitVisible("#context-menu", chromedp.ByQuery),
		chromedp.Click(`#context-menu [role="menuitem"]:first-child`, chromedp.ByQuery),
		chromedp.WaitVisible("#service-dialog", chromedp.ByQuery),
		chromedp.Evaluate(`(() => ({
          editEndpoint: document.querySelector('#service-form input[name="displayUrl"]').value,
          editIcon: document.querySelector('#service-form input[name="icon"]').value
        }))()`, &report),
		chromedp.KeyEvent("\x1b"),
		chromedp.Poll(`!document.querySelector('#service-dialog').open`, nil, chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Evaluate(`document.querySelector('.service-card[data-service-id^="demo-"] .service-menu-button').click()`, nil),
		chromedp.WaitVisible("#context-menu", chromedp.ByQuery),
		chromedp.Click(`#context-menu [role="menuitem"]:first-child`, chromedp.ByQuery),
		chromedp.WaitVisible("#service-dialog", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('#service-form input[name="icon"]').value`, &report.EditEmptyIcon),
		chromedp.KeyEvent("\x1b"),
		chromedp.Poll(`!document.querySelector('#service-dialog').open`, nil, chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Focus(".service-link", chromedp.ByQuery),
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
		chromedp.Click(`[data-workspace="containers"]`, chromedp.ByQuery),
		chromedp.WaitVisible(".container-item .container-action:not(:disabled)", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.container-item .container-action:not(:disabled)').focus()`, nil),
		chromedp.Poll(`document.activeElement?.matches('.container-item .container-action:not(:disabled)') === true`, nil, chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Evaluate(`window.__terminalInvoker = document.activeElement`, nil),
		chromedp.KeyEvent("\r"),
		chromedp.WaitVisible("#terminal-disconnect", chromedp.ByQuery),
		chromedp.WaitVisible("#log-viewer", chromedp.ByQuery),
		chromedp.Sleep(150*time.Millisecond),
		chromedp.Evaluate(`(() => { const input = document.querySelector('#log-search'); input.value = 'HEALTH'; input.dispatchEvent(new Event('input', { bubbles: true })); })()`, nil),
		chromedp.Evaluate(`document.querySelectorAll('#log-output .log-line:not([hidden])').length`, &report.LogSearchMatches),
		chromedp.Click("#log-pause", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('#log-pause').getAttribute('aria-pressed')`, &report.LogPausePressed),
		chromedp.Click("#log-pause", chromedp.ByQuery),
		chromedp.Evaluate(`(() => { const input = document.querySelector('#log-search'); input.value = ''; input.dispatchEvent(new Event('input', { bubbles: true })); })()`, nil),
		chromedp.Click("#log-follow", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('#log-follow').getAttribute('aria-pressed')`, &report.LogFollowPressed),
		chromedp.Click("#log-follow", chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
          window.__logDownload = { name: '', bytes: 0 };
          window.__createObjectURL = URL.createObjectURL;
          window.__revokeObjectURL = URL.revokeObjectURL;
          window.__anchorClick = HTMLAnchorElement.prototype.click;
          URL.createObjectURL = (blob) => { window.__logDownload.bytes = blob.size; return 'blob:log-test'; };
          URL.revokeObjectURL = () => {};
          HTMLAnchorElement.prototype.click = function () { window.__logDownload.name = this.download; };
        })()`, nil),
		chromedp.Click("#log-download", chromedp.ByQuery),
		chromedp.Sleep(25*time.Millisecond),
		chromedp.Evaluate(`(() => {
          const result = { logDownloadName: window.__logDownload.name, logDownloadBytes: window.__logDownload.bytes };
          URL.createObjectURL = window.__createObjectURL;
          URL.revokeObjectURL = window.__revokeObjectURL;
          HTMLAnchorElement.prototype.click = window.__anchorClick;
          return result;
        })()`, &report),
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
          terminalOutput: document.querySelector('#log-output')?.textContent || '',
          terminalModeLogs: document.querySelector('#terminal-panel').classList.contains('terminal-mode-logs'),
          logViewerVisible: !document.querySelector('#log-viewer').hidden,
	          xtermHidden: document.querySelector('#terminal').hidden,
	          disconnectIsVisible: !document.querySelector('#terminal-disconnect').hidden,
	          partialBadgeVisible: !document.querySelector('#snapshot-partial').hidden,
	          overviewHealth: document.querySelector('#overview-health').textContent.trim()
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
	if !report.PartialBadgeVisible || report.OverviewHealth != "PARTIAL" {
		t.Fatalf("truncated snapshot was presented as complete: %+v", report)
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
	if report.EditEndpoint != "https://immich.homelab.ts.net" || report.EditIcon != "📸" {
		t.Fatalf("edit service did not preserve its hidden compatibility fields: %+v", report)
	}
	if report.EditEmptyIcon != "" {
		t.Fatalf("edit service did not preserve an intentionally empty icon: %+v", report)
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
	if !report.LogViewerVisible || !report.XtermHidden || report.LogSearchMatches != 1 || report.LogPausePressed != "true" || report.LogFollowPressed != "false" || report.LogDownloadBytes < 1 || !strings.HasSuffix(report.LogDownloadName, "-logs.txt") {
		t.Fatalf("dedicated log viewer controls failed: %+v", report)
	}
	if report.CollapsedHeight != 0 || !report.InvokerFocusRestored {
		t.Fatalf("terminal collapse did not restore an unobscured invoker focus: %+v", report)
	}
}

type offlineReport struct {
	Offline            bool   `json:"offline"`
	SystemTitle        string `json:"systemTitle"`
	SystemHostname     string `json:"systemHostname"`
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
	          systemHostname: document.querySelector('#system-hostname').textContent,
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
	if !report.Offline || report.SystemTitle != "System health and metrics" || report.SystemHostname != "Unable to reach server" || !report.BannerVisible || !strings.Contains(report.BannerMessage, "Unable to reach") {
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
	UnavailableNotice string `json:"unavailableNotice"`
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
		chromedp.Evaluate(`(() => ({
          unavailableLabel: document.querySelector('#terminal-session-label').textContent,
          unavailableNotice: [document.querySelector('.xterm-rows')?.textContent || '', document.querySelector('#toast-region .toast')?.textContent || ''].join(' ')
        }))()`, &report),
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
	if !strings.Contains(report.UnavailableNotice, "host_agent_unavailable") || !strings.Contains(report.UnavailableNotice, "HTTP 503") {
		t.Fatalf("host shell API diagnostics were not surfaced: %+v", report)
	}
	if report.LostLabel != "HOST · DISCONNECTED" || report.LostStillLabel != "HOST · DISCONNECTED" || !report.LostButtonHidden {
		t.Fatalf("lost host shell was silently reopened: %+v", report)
	}
}

func TestDemoMonitoringWorkbench(t *testing.T) {
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

	var browserFailures []string
	chromedp.ListenTarget(ctx, func(event any) {
		switch value := event.(type) {
		case *cdpruntime.EventExceptionThrown:
			browserFailures = append(browserFailures, value.ExceptionDetails.Text)
		case *cdpruntime.EventConsoleAPICalled:
			if value.Type == cdpruntime.APITypeError {
				browserFailures = append(browserFailures, "console.error")
			}
		}
	})

	var report struct {
		HistoryPoints          int    `json:"historyPoints"`
		ContainerHistoryPoints int    `json:"containerHistoryPoints"`
		ServiceHistoryPoints   int    `json:"serviceHistoryPoints"`
		HistoryResolution      string `json:"historyResolution"`
		NodeOptions            int    `json:"nodeOptions"`
		RuleCount              int    `json:"ruleCount"`
		NtfyConfigured         bool   `json:"ntfyConfigured"`
		AlertFocusRestored     bool   `json:"alertFocusRestored"`
		PreviewReady           bool   `json:"previewReady"`
		ApplyDisabledBefore    bool   `json:"applyDisabledBefore"`
		ImportApplied          bool   `json:"importApplied"`
		TokenVisible           bool   `json:"tokenVisible"`
		TokenCleared           bool   `json:"tokenCleared"`
		RemoteTruthful         bool   `json:"remoteTruthful"`
	}

	err = chromedp.Run(ctx,
		chromedp.EmulateViewport(1440, 900),
		chromedp.Navigate(server.URL+"/?demo=1"),
		chromedp.Click(`[data-workspace="history"]`, chromedp.ByQuery),
		chromedp.WaitVisible("#history-panel", chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('#history-empty').hidden`, nil, chromedp.WithPollingTimeout(3*time.Second)),
		chromedp.Evaluate(`(() => ({
          historyPoints: Chart.getChart(document.querySelector('#history-chart'))?.data?.datasets?.[0]?.data?.length || 0,
          historyResolution: document.querySelector('#history-resolution').textContent,
          nodeOptions: document.querySelector('#node-selector').options.length
        }))()`, &report),
		chromedp.Click(`[data-history-kind="container"]`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('#history-resource').options.length > 0 && document.querySelector('#history-empty').hidden`, nil, chromedp.WithPollingTimeout(3*time.Second)),
		chromedp.Evaluate(`Chart.getChart(document.querySelector('#history-chart'))?.data?.datasets?.[0]?.data?.length || 0`, &report.ContainerHistoryPoints),
		chromedp.Click(`[data-history-kind="service"]`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('#history-resource').options.length > 0 && document.querySelector('#history-empty').hidden`, nil, chromedp.WithPollingTimeout(3*time.Second)),
		chromedp.Evaluate(`Chart.getChart(document.querySelector('#history-chart'))?.data?.datasets?.[0]?.data?.length || 0`, &report.ServiceHistoryPoints),
		chromedp.Click(`[data-history-kind="system"]`, chromedp.ByQuery),
		chromedp.Click("#alerts-jump", chromedp.ByQuery),
		chromedp.WaitVisible("#alert-center-dialog", chromedp.ByQuery),
		chromedp.Poll(`document.querySelectorAll('#alert-rules-list .management-item').length === 2`, nil, chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Click("#alert-rule-add", chromedp.ByQuery),
		chromedp.WaitVisible("#alert-rule-dialog", chromedp.ByQuery),
		chromedp.SetValue(`#alert-rule-form input[name="name"]`, "Memory pressure", chromedp.ByQuery),
		chromedp.SetValue(`#alert-rule-form select[name="metric"]`, "system.memory.percent", chromedp.ByQuery),
		chromedp.Click("#alert-rule-submit", chromedp.ByQuery),
		chromedp.Poll(`!document.querySelector('#alert-rule-dialog').open && document.querySelectorAll('#alert-rules-list .management-item').length === 3`, nil, chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Evaluate(`(() => ({
          ruleCount: document.querySelectorAll('#alert-rules-list .management-item').length,
          ntfyConfigured: document.querySelector('#ntfy-status').dataset.configured === 'true'
        }))()`, &report),
		chromedp.KeyEvent("\x1b"),
		chromedp.Poll(`!document.querySelector('#alert-center-dialog').open`, nil, chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Evaluate(`document.activeElement === document.querySelector('#alerts-jump')`, &report.AlertFocusRestored),
		chromedp.Click("#settings-open", chromedp.ByQuery),
		chromedp.WaitVisible("#settings-dialog", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('#config-apply').disabled`, &report.ApplyDisabledBefore),
		chromedp.Evaluate(`(() => {
          const config = { version: 'homelab-dashboard.config/v1', services: [], alertRules: [], uiPreferences: { terminalHeight: 200, terminalCollapsed: true, historyRange: '24h', defaultNodeId: 'local' }, nodes: [] };
          const transfer = new DataTransfer();
          transfer.items.add(new File([JSON.stringify(config)], 'config.json', { type: 'application/json' }));
          const input = document.querySelector('#config-file');
          input.files = transfer.files;
          input.dispatchEvent(new Event('change', { bubbles: true }));
        })()`, nil),
		chromedp.Poll(`!document.querySelector('#config-preview').disabled`, nil, chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Click("#config-preview", chromedp.ByQuery),
		chromedp.Poll(`!document.querySelector('#config-apply').disabled`, nil, chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Evaluate(`document.querySelector('#config-preview-result dl') !== null`, &report.PreviewReady),
		chromedp.Click("#config-apply", chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('#settings-status').textContent.includes('applied successfully')`, nil, chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Evaluate(`document.querySelector('#settings-status').textContent.includes('applied successfully')`, &report.ImportApplied),
		chromedp.Click("#settings-dialog [data-dialog-close]", chromedp.ByQuery),
		chromedp.Click("#nodes-open", chromedp.ByQuery),
		chromedp.WaitVisible("#nodes-dialog", chromedp.ByQuery),
		chromedp.Click("#node-enroll-create", chromedp.ByQuery),
		chromedp.Poll(`!document.querySelector('#enrollment-result').hidden`, nil, chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Evaluate(`!document.querySelector('#enrollment-result').hidden && document.querySelector('#enrollment-token').textContent.startsWith('enroll_')`, &report.TokenVisible),
		chromedp.Click("#nodes-dialog [data-dialog-close]", chromedp.ByQuery),
		chromedp.Click("#nodes-open", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('#enrollment-result').hidden && document.querySelector('#enrollment-token').textContent === ''`, &report.TokenCleared),
		chromedp.Click("#nodes-dialog [data-dialog-close]", chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
          const selector = document.querySelector('#node-selector');
          selector.value = 'node_demo';
          selector.dispatchEvent(new Event('change', { bubbles: true }));
        })()`, nil),
		chromedp.Poll(`document.body.classList.contains('remote-node')`, nil, chromedp.WithPollingTimeout(2*time.Second)),
		chromedp.Evaluate(`(() => {
          const actions = document.querySelector('.container-actions');
          return document.body.classList.contains('remote-node') && document.querySelector('#freshness-text').textContent === 'OFFLINE' && (!actions || getComputedStyle(actions).display === 'none');
        })()`, &report.RemoteTruthful),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(browserFailures) > 0 {
		t.Fatalf("browser failures: %s", strings.Join(browserFailures, "; "))
	}
	if report.HistoryPoints == 0 || report.ContainerHistoryPoints == 0 || report.ServiceHistoryPoints == 0 ||
		!strings.Contains(report.HistoryResolution, "RESOLUTION") || report.NodeOptions != 2 {
		t.Fatalf("history/node selector did not initialize: %+v", report)
	}
	if report.RuleCount != 3 || !report.NtfyConfigured || !report.AlertFocusRestored {
		t.Fatalf("alert center CRUD, ntfy state, or focus restoration failed: %+v", report)
	}
	if !report.ApplyDisabledBefore || !report.PreviewReady || !report.ImportApplied {
		t.Fatalf("configuration preview-before-apply flow failed: %+v", report)
	}
	if !report.TokenVisible || !report.TokenCleared || !report.RemoteTruthful {
		t.Fatalf("node enrollment secrecy or remote offline state failed: %+v", report)
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

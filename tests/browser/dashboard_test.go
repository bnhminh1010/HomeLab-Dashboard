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
	HasXterm        bool     `json:"hasXterm"`
	Overflow        []string `json:"overflow"`
}

func TestDemoDashboardResponsiveAndOffline(t *testing.T) {
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
		check         func(*testing.T, layoutReport)
	}{
		{"desktop", 1440, 900, func(t *testing.T, report layoutReport) {
			if !(report.LeftX < report.CenterX && report.CenterX < report.RightX) {
				t.Fatalf("desktop columns are not ordered: %+v", report)
			}
		}},
		{"tablet", 1024, 768, func(t *testing.T, report layoutReport) {
			if !(report.CenterY > report.LeftY && report.CenterX < report.RightX && near(report.CenterY, report.RightY, 4)) {
				t.Fatalf("tablet layout is not system-full-row plus two columns: %+v", report)
			}
			if report.SystemColumns != 3 {
				t.Fatalf("tablet compact system columns=%d", report.SystemColumns)
			}
		}},
		{"mobile", 640, 844, func(t *testing.T, report layoutReport) {
			if !(report.CenterY >= report.LeftBottom && report.RightY >= report.CenterBottom) || report.ServicesColumns != 1 {
				t.Fatalf("mobile layout is not a one-column stack: %+v", report)
			}
			if report.SystemColumns != 2 {
				t.Fatalf("mobile compact system columns=%d", report.SystemColumns)
			}
			if report.TerminalHeight < 140 || report.TerminalHeight > 160 {
				t.Fatalf("mobile terminal height=%v", report.TerminalHeight)
			}
		}},
	}

	for _, viewport := range viewports {
		viewport := viewport
		t.Run(viewport.name, func(t *testing.T) {
			allocatorOptions := append(chromedp.DefaultExecAllocatorOptions[:],
				chromedp.ExecPath(chrome), chromedp.Flag("no-sandbox", true), chromedp.Flag("disable-gpu", true))
			allocator, cancelAllocator := chromedp.NewExecAllocator(context.Background(), allocatorOptions...)
			defer cancelAllocator()
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
				chromedp.WaitVisible(".xterm", chromedp.ByQuery),
				chromedp.Sleep(1200*time.Millisecond),
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
            terminalHeight: rect('#terminal-body').height,
            hasXterm: Boolean(document.querySelector('.xterm .xterm-helper-textarea')),
            overflow: [...document.querySelectorAll('body *')]
              .filter((node) => node.getBoundingClientRect().right > window.innerWidth + 1)
              .slice(0, 10)
              .map((node) => node.tagName.toLowerCase() + '#' + node.id + '.' + String(node.className))
          };
        })()`, &report),
			)
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(failures) > 0 {
				t.Fatalf("browser failures: %s", strings.Join(failures, "; "))
			}
			if report.ScrollWidth > report.ViewportWidth+1 {
				t.Fatalf("horizontal overflow: %+v", report)
			}
			if report.Services < 2 || report.Containers < 3 || !report.HasXterm {
				t.Fatalf("demo acceptance data/xterm missing: %+v", report)
			}
			viewport.check(t, report)
		})
	}
}

type interactionReport struct {
	CPUText             string  `json:"cpuText"`
	CPUMax              string  `json:"cpuMax"`
	CPUProgress         string  `json:"cpuProgress"`
	CPUDetail           string  `json:"cpuDetail"`
	RAMText             string  `json:"ramText"`
	RAMOverLimit        bool    `json:"ramOverLimit"`
	DiskWarningVisible  bool    `json:"diskWarningVisible"`
	DiskLevel           string  `json:"diskLevel"`
	CrashedContainers   int     `json:"crashedContainers"`
	StoppedContainers   int     `json:"stoppedContainers"`
	Services            int     `json:"services"`
	AddedEndpoint       string  `json:"addedEndpoint"`
	OpenedURL           string  `json:"openedUrl"`
	TerminalSession     string  `json:"terminalSession"`
	TerminalOutput      string  `json:"terminalOutput"`
	CollapsedHeight     float64 `json:"collapsedHeight"`
	ExpandedHeight      float64 `json:"expandedHeight"`
	DisconnectIsVisible bool    `json:"disconnectIsVisible"`
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
		chromedp.WaitVisible(".xterm", chromedp.ByQuery),
		chromedp.SetValue("#quick-name", "Port service", chromedp.ByQuery),
		chromedp.SetValue(`#quick-add-form input[name="displayUrl"]`, "9090", chromedp.ByQuery),
		chromedp.Click(`#quick-add-form button[type="submit"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.service-card[data-service-id^="demo-"]`, chromedp.ByQuery),
		chromedp.Evaluate(`window.__openedService = ""; window.open = (url) => { window.__openedService = String(url); return null; }; document.querySelector('.service-card').dispatchEvent(new MouseEvent('click', { bubbles: true }));`, nil),
		chromedp.Click(".container-item .container-action", chromedp.ByQuery),
		chromedp.WaitVisible("#terminal-disconnect", chromedp.ByQuery),
		chromedp.Click("#terminal-toggle", chromedp.ByQuery),
		chromedp.Sleep(250*time.Millisecond),
		chromedp.Evaluate(`document.querySelector('#terminal-body').getBoundingClientRect().height`, &report.CollapsedHeight),
		chromedp.Click("#terminal-toggle", chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
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
          addedEndpoint: [...document.querySelectorAll('.service-card')].find((node) => node.textContent.includes('Port service'))?.querySelector('.service-link span:last-child')?.textContent || '',
          openedUrl: window.__openedService || '',
          terminalSession: document.querySelector('#terminal-session-label').textContent,
          terminalOutput: document.querySelector('.xterm-rows')?.textContent || '',
          expandedHeight: document.querySelector('#terminal-body').getBoundingClientRect().height,
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
	if !strings.Contains(report.OpenedURL, "immich.homelab.ts.net") {
		t.Fatalf("whole-card service navigation failed: %+v", report)
	}
	if !strings.Contains(report.TerminalSession, "LOGS") || !strings.Contains(report.TerminalOutput, "service ready") || !report.DisconnectIsVisible {
		t.Fatalf("container logs terminal failed: %+v", report)
	}
	if report.CollapsedHeight != 0 || report.ExpandedHeight < 190 {
		t.Fatalf("terminal collapse/expand failed: %+v", report)
	}
}

type offlineReport struct {
	Offline            bool   `json:"offline"`
	SystemTitle        string `json:"systemTitle"`
	BannerVisible      bool   `json:"bannerVisible"`
	BannerMessage      string `json:"bannerMessage"`
	ServiceSkeletons   int    `json:"serviceSkeletons"`
	ContainerSkeletons int    `json:"containerSkeletons"`
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
          containerSkeletons: document.querySelectorAll('.container-item.skeleton-container').length
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

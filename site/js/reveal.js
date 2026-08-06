/* ─── Landing micro-interactions ───
   KPI count-up, quickstart typing loop, nav shrink on scroll.
   No scroll-triggered reveal: sections render statically (anti-slop
   rule — the page settles after the initial hero entrance, which is
   a pure CSS animation in style.css). */

(function () {
  "use strict";
  if (window.__landingInit) return;
  window.__landingInit = true;

  const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  const opts = { threshold: 0.4 };

  // ── nav shrink on scroll ──
  const nav = document.getElementById("nav");
  if (nav) {
    const onScroll = () => nav.classList.toggle("is-scrolled", window.scrollY > 24);
    onScroll();
    window.addEventListener("scroll", onScroll, { passive: true });
  }

  // ── KPI count-up (one-shot when visible) ──
  const kpiCells = document.querySelectorAll(".kpi-num[data-count]");
  if (kpiCells.length) {
    const io = new IntersectionObserver((entries) => {
      for (const e of entries) {
        if (!e.isIntersecting) continue;
        const el = e.target;
        io.unobserve(el);
        const target = parseInt(el.dataset.count, 10);
        const suffix = el.dataset.suffix || "";
        if (reduced || target === 0) {
          el.textContent = target + suffix;
          continue;
        }
        const t0 = performance.now();
        const dur = 900;
        const step = (now) => {
          const p = Math.min(1, (now - t0) / dur);
          const eased = 1 - Math.pow(1 - p, 3);
          el.textContent = Math.round(target * eased) + suffix;
          if (p < 1) requestAnimationFrame(step);
        };
        requestAnimationFrame(step);
      }
    }, opts);
    kpiCells.forEach((c) => io.observe(c));
  }

  // ── quickstart typing loop ──
  const typer = document.getElementById("typer");
  if (typer) {
    const lines = [
      { p: "$", t: "curl -fsSL https://homelab.sh/install | sh" },
      { p: "$", t: "sudo systemctl start homelab-dashboard" },
      { p: "→", t: "https://homelab-01.tailnet.ts.net" },
    ];
    let li = 0, ci = 0, phase = "type";
    const typeIo = new IntersectionObserver((entries) => {
      for (const e of entries) {
        if (e.isIntersecting) {
          typeIo.disconnect();
          tick();
        }
      }
    }, { threshold: 0.3 });
    typeIo.observe(typer);

    function tick() {
      if (phase === "type") {
        const line = lines[li];
        const cur = line.t.slice(0, ++ci);
        typer.innerHTML =
          '<span class="c-prompt">' + line.p + "</span> " +
          '<span class="c-cmd">' + esc(cur) + "</span><span class=\"c-cursor\"></span>";
        if (ci >= line.t.length) { phase = "pause"; setTimeout(tick, 650); }
        else setTimeout(tick, 18 + Math.random() * 30);
      } else if (phase === "pause") {
        phase = "erase";
        setTimeout(tick, 60);
      } else if (phase === "erase") {
        const line = lines[li];
        ci = Math.max(0, ci - 3);
        const cur = line.t.slice(0, ci);
        typer.innerHTML =
          '<span class="c-prompt">' + line.p + "</span> " +
          '<span class="c-cmd">' + esc(cur) + "</span><span class=\"c-cursor\"></span>";
        if (ci <= 0) {
          li = (li + 1) % lines.length;
          phase = "type";
        }
        setTimeout(tick, 16);
      }
    }

    function esc(s) {
      return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
    }
  }

  // ── demo iframe: hide the inner scrollbar (wheel scrolling still works) ──
  // The dashboard grid is a scroll container by design; inside the embedded
  // demo frame that shows a visible scrollbar which reads as sloppy. We
  // inject a small style into the same-origin frame document instead of
  // touching the app's own CSS.
  const demoFrame = document.querySelector('.demo-frame iframe');
  if (demoFrame) {
    const hideScroller = () => {
      try {
        const doc = demoFrame.contentDocument;
        // Lazy-loaded frames start as about:blank — only touch the real document.
        if (!doc || doc.URL === "about:blank" || !doc.head) return;
        const st = doc.createElement("style");
        st.textContent =
          ".dashboard-grid { scrollbar-width: none; -ms-overflow-style: none; }" +
          ".dashboard-grid::-webkit-scrollbar { display: none; }";
        doc.head.appendChild(st);
      } catch (e) { /* cross-origin or not ready — harmless */ }
    };
    // Always hook the load event: with loading="lazy" the contentDocument is
    // about:blank at init time and only becomes the real document on load.
    demoFrame.addEventListener("load", hideScroller);
    if (demoFrame.contentDocument && demoFrame.contentDocument.URL !== "about:blank") {
      hideScroller();
    }
  }
})();

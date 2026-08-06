/* ─── Scroll reveal + KPI count-up + quickstart typing ───
   One IntersectionObserver for everything; respects prefers-reduced-motion.
   Count-up fires once per KPI cell; typing loop is slow, not eager. */
(function () {
  const mq = window.matchMedia("(prefers-reduced-motion: reduce)");
  const reduced = mq.matches;

  // ── reveal ──
  const revealEls = document.querySelectorAll(".reveal");
  if (reduced || !("IntersectionObserver" in window)) {
    revealEls.forEach((el) => el.classList.add("is-in"));
  } else {
    const io = new IntersectionObserver(
      (entries) => {
        for (const en of entries) {
          if (en.isIntersecting) {
            en.target.classList.add("is-in");
            io.unobserve(en.target);
          }
        }
      },
      { threshold: 0.12, rootMargin: "0px 0px -40px 0px" }
    );
    revealEls.forEach((el, i) => {
      el.style.transitionDelay = Math.min(i % 6, 5) * 40 + "ms";
      io.observe(el);
    });
  }

  // ── KPI count-up (once) ──
  const nums = document.querySelectorAll(".kpi-num[data-count]");
  function countUp(el) {
    const target = parseInt(el.dataset.count, 10);
    const suffix = el.dataset.suffix || "";
    const dur = 600;
    const t0 = performance.now();
    function tick(now) {
      const p = Math.min(1, (now - t0) / dur);
      const eased = 1 - Math.pow(1 - p, 3); // ease-out cubic
      el.textContent = Math.round(target * eased) + suffix;
      if (p < 1) requestAnimationFrame(tick);
    }
    requestAnimationFrame(tick);
  }
  if (!reduced && "IntersectionObserver" in window) {
    const ioN = new IntersectionObserver(
      (entries) => {
        for (const en of entries) {
          if (en.isIntersecting) {
            countUp(en.target);
            ioN.unobserve(en.target);
          }
        }
      },
      { threshold: 0.6 }
    );
    nums.forEach((n) => ioN.observe(n));
  } else {
    nums.forEach((n) => { n.textContent = n.dataset.count + (n.dataset.suffix || ""); });
  }

  // ── quickstart typing loop ──
  const pre = document.getElementById("typer");
  if (pre) {
    const lines = [
      { prompt: "$", cmd: "curl -fsSL homelab-dash.dev/install | bash" },
      { prompt: "$", cmd: "podman compose up -d" },
      { prompt: "$", cmd: "tailscale serve 8082" },
      { prompt: "", cmd: "→ open https://your-node.tailXXX.ts.net", out: true }
    ];
    let li = 0, ci = 0, deleting = false;
    const CURSOR = '<span class="c-cursor"></span>';

    function esc(s) {
      return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
    }
    function lineHtml(l) {
      if (l.out) return '<span class="c-out">' + esc(l.cmd) + "</span>";
      return '<span class="c-prompt">' + l.prompt + '</span> <span class="c-cmd">' + esc(l.cmd) + "</span>";
    }
    function render() {
      const head = lines.slice(0, li).map(lineHtml).join("\n");
      const line = lines[li];
      let body;
      if (line.out) {
        body = lineHtml(line);
      } else {
        body = '<span class="c-prompt">' + line.prompt + '</span> <span class="c-cmd">' + esc(line.cmd.slice(0, ci)) + "</span>";
      }
      pre.innerHTML = head + (li > 0 ? "\n" : "") + body + CURSOR;
    }

    let started = false;
    function step() {
      const line = lines[li];
      if (line.out) {
        li = (li + 1) % lines.length;
        ci = 0;
        render();
        return setTimeout(step, 1400);
      }
      if (!deleting) {
        ci++;
        if (ci > line.cmd.length) {
          deleting = true;
          render();
          return setTimeout(step, 700); // pause on full line
        }
      } else {
        ci--;
        if (ci <= 0) { deleting = false; li = (li + 1) % lines.length; }
      }
      render();
      setTimeout(step, deleting ? 14 : 55);
    }

    if (reduced) {
      pre.innerHTML = lines.map(lineHtml).join("\n");
    } else if ("IntersectionObserver" in window) {
      const ioT = new IntersectionObserver(
        (entries) => {
          if (entries[0].isIntersecting && !started) {
            started = true;
            step();
            ioT.disconnect();
          }
        },
        { threshold: 0.4 }
      );
      ioT.observe(pre);
    }
  }
})();

/* ─── Dot-wave hero (prototype v3 spec) ───
   Dots #e5e1d9, alpha 0.16–0.95, spacing 34px staggered,
   magnetic repel 220px / MOUSE_POWER 26, ambient wave 2.5px,
   fades out as the hero scrolls away. Single motion hero. */
(function () {
  const canvas = document.getElementById("wave");
  if (!canvas) return;
  const ctx = canvas.getContext("2d");
  const DPR = Math.min(window.devicePixelRatio || 1, 2);
  const SPACING = 34;
  const REPEL = 220;
  const MOUSE_POWER = 26;
  const AMBIENT = 2.5;
  const COLOR = "229,225,217"; // #e5e1d9

  let W = 0, H = 0, cols = 0, rows = 0;
  let dots = [];
  let mouse = { x: -9999, y: -9999, active: false };
  let raf = 0;
  let t = 0;

  function resize() {
    const rect = canvas.parentElement.getBoundingClientRect();
    W = rect.width; H = rect.height;
    canvas.width = W * DPR; canvas.height = H * DPR;
    canvas.style.width = W + "px"; canvas.style.height = H + "px";
    ctx.setTransform(DPR, 0, 0, DPR, 0, 0);
    cols = Math.ceil(W / SPACING);
    rows = Math.ceil(H / SPACING);
    dots = [];
    for (let r = 0; r < rows; r++) {
      for (let c = 0; c < cols; c++) {
        dots.push({
          bx: c * SPACING + SPACING / 2,
          by: r * SPACING + SPACING / 2,
          off: (r % 2) * 6,           // staggered
          ph: Math.random() * Math.PI * 2,
          sp: 0.4 + Math.random() * 0.5,
          amp: 0.6 + Math.random() * 1.4
        });
      }
    }
  }

  function frame() {
    t += 0.016;
    const hero = canvas.parentElement;
    const rect = hero.getBoundingClientRect();
    // fade as hero scrolls: 1 at top → 0.15 when hero top reaches viewport bottom
    const progress = Math.min(1, Math.max(0, rect.bottom / window.innerHeight));
    ctx.clearRect(0, 0, W, H);

    for (let i = 0; i < dots.length; i++) {
      const d = dots[i];
      // ambient wave
      const sway = Math.sin(t * d.sp + d.ph) * d.amp * AMBIENT;
      let x = d.bx + sway * 0.4;
      let y = d.by + Math.cos(t * d.sp * 0.8 + d.ph * 1.3) * d.amp * 0.35;
      // magnetic repel
      const dx = x - mouse.x, dy = y - mouse.y;
      const dist2 = dx * dx + dy * dy;
      if (dist2 < REPEL * REPEL && mouse.active) {
        const dist = Math.sqrt(dist2) || 1;
        const f = (1 - dist / REPEL) * MOUSE_POWER;
        x += (dx / dist) * f;
        y += (dy / dist) * f;
      }
      // alpha by distance from center band + fade with scroll
      const band = 1 - Math.min(1, Math.abs(y - H / 2) / (H / 2)) * 0.8;
      const a = (0.16 + 0.79 * band) * progress;
      if (a < 0.02) continue;
      ctx.beginPath();
      ctx.arc(x, y, d.off ? 1.5 : 1.15, 0, Math.PI * 2);
      ctx.fillStyle = "rgba(" + COLOR + "," + a.toFixed(3) + ")";
      ctx.fill();
    }
    raf = requestAnimationFrame(frame);
  }

  function onMove(e) {
    const rect = canvas.getBoundingClientRect();
    mouse.x = e.clientX - rect.left;
    mouse.y = e.clientY - rect.top;
    mouse.active = true;
  }
  function onLeave() { mouse.active = false; mouse.x = -9999; mouse.y = -9999; }
  function onScroll() { /* read in frame() */ }

  const mq = window.matchMedia("(prefers-reduced-motion: reduce)");
  function start() {
    resize();
    if (mq.matches) {
      // static: draw one frame, no loop
      frame();
      cancelAnimationFrame(raf);
    } else {
      frame();
    }
  }
  function stop() { cancelAnimationFrame(raf); }

  window.addEventListener("resize", resize);
  window.addEventListener("mousemove", onMove, { passive: true });
  window.addEventListener("mouseleave", onLeave);
  window.addEventListener("scroll", onScroll, { passive: true });
  mq.addEventListener("change", start);
  start();
})();

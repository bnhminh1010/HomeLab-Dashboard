/* ─── Dot-wave hero (prototype v3 spec, tuned) ───
   Dots #e5e1d9, alpha 0.16–0.95, spacing 34px staggered,
   magnetic repel ~150px with soft falloff, spring-back lerp,
   ambient wave 2.5px, fades out as the hero scrolls away.
   Tuning follows the reference style (X.com dot wave / Framer
   Interactive Dot Wave): dots drift gently and ease back — they
   never snap. */
(function () {
  const canvas = document.getElementById("wave");
  if (!canvas) return;
  const ctx = canvas.getContext("2d");
  const DPR = Math.min(window.devicePixelRatio || 1, 2);
  const SPACING = 34;
  const REPEL = 150;         // mouse influence radius (px)
  const PUSH = 12;           // max displacement at center (px)
  const LERP = 0.06;         // spring-back speed toward target
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
          bx: c * SPACING + SPACING / 2,   // base x
          by: r * SPACING + SPACING / 2,   // base y
          x: c * SPACING + SPACING / 2,    // current x (lerped)
          y: r * SPACING + SPACING / 2,    // current y (lerped)
          off: (r % 2) * 6,                // staggered
          ph: Math.random() * Math.PI * 2,
          sp: 0.4 + Math.random() * 0.5,
          amp: 0.6 + Math.random() * 1.4
        });
      }
    }
  }

  function frame() {
    t += 0.016;
    const rect = canvas.parentElement.getBoundingClientRect();
    // fade as hero scrolls: 1 at top → 0.15 when hero top reaches viewport bottom
    const progress = Math.min(1, Math.max(0, rect.bottom / window.innerHeight));
    ctx.clearRect(0, 0, W, H);

    for (let i = 0; i < dots.length; i++) {
      const d = dots[i];
      // ambient wave (target position)
      const sway = Math.sin(t * d.sp + d.ph) * d.amp * AMBIENT;
      let tx = d.bx + sway * 0.4;
      let ty = d.by + Math.cos(t * d.sp * 0.8 + d.ph * 1.3) * d.amp * 0.35;
      // mouse repel — soft falloff, capped displacement
      if (mouse.active) {
        const dx = d.bx - mouse.x, dy = d.by - mouse.y;
        const dist = Math.sqrt(dx * dx + dy * dy);
        if (dist < REPEL && dist > 0.01) {
          const fall = (1 - dist / REPEL);
          const push = PUSH * fall * fall;   // quadratic: gentle at edge
          tx += (dx / dist) * push;
          ty += (dy / dist) * push;
        }
      }
      // spring-back: ease current toward target, never snap
      d.x += (tx - d.x) * LERP;
      d.y += (ty - d.y) * LERP;

      // alpha by distance from center band + fade with scroll
      const band = 1 - Math.min(1, Math.abs(d.y - H / 2) / (H / 2)) * 0.8;
      const a = (0.16 + 0.79 * band) * progress;
      if (a < 0.02) continue;
      ctx.beginPath();
      ctx.arc(d.x, d.y, d.off ? 1.5 : 1.15, 0, Math.PI * 2);
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
  mq.addEventListener("change", start);
  start();
})();

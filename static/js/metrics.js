import { clamp } from "./format.js";

const MAX_POINTS = 60;

function chartOptions() {
  return {
    // Fixed backing dimensions avoid Chart.js using the whole metrics card as
    // its responsive parent. CSS still scales the tiny sparkline to the card.
    responsive: false,
    maintainAspectRatio: false,
    animation: false,
    parsing: false,
    normalized: true,
    interaction: { enabled: false },
    scales: {
      x: { display: false, type: "linear" },
      y: { display: false, min: 0, max: 100 },
    },
    plugins: { legend: { display: false }, tooltip: { enabled: false } },
    elements: { point: { radius: 0 }, line: { borderWidth: 1.5, tension: 0.25 } },
  };
}

function createChart(canvas, color, fill) {
  if (!canvas || typeof globalThis.Chart !== "function") return null;
  return new globalThis.Chart(canvas.getContext("2d"), {
    type: "line",
    data: {
      datasets: [{ data: [], borderColor: color, backgroundColor: fill, fill: true }],
    },
    options: chartOptions(),
  });
}

function append(chart, value, fallbackHistory) {
  const point = { x: Date.now(), y: clamp(value) };
  if (chart) {
    const data = chart.data.datasets[0].data;
    data.push(point);
    if (data.length > MAX_POINTS) data.splice(0, data.length - MAX_POINTS);
    chart.update("none");
    return;
  }
  fallbackHistory.push(point.y);
  if (fallbackHistory.length > MAX_POINTS) fallbackHistory.shift();
}

export function createMetricCharts() {
  const cpuCanvas = document.getElementById("cpu-chart");
  const ramCanvas = document.getElementById("ram-chart");
  const cpuChart = createChart(cpuCanvas, "#6ee2ff", "rgba(110,226,255,.08)");
  const ramChart = createChart(ramCanvas, "#22c55e", "rgba(34,197,94,.07)");
  const cpuFallback = [];
  const ramFallback = [];

  return {
    update(cpu, ram) {
      append(cpuChart, cpu, cpuFallback);
      append(ramChart, ram, ramFallback);
    },
    destroy() {
      cpuChart?.destroy();
      ramChart?.destroy();
    },
  };
}

import { clamp } from "./format.js";

const MAX_POINTS = 60;

function colorToken(name) {
  return globalThis.getComputedStyle?.(document.documentElement).getPropertyValue(name).trim() || "transparent";
}

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
    elements: { point: { radius: 0 }, line: { borderWidth: 2, tension: 0.25 } },
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
  const cpuChart = createChart(cpuCanvas, colorToken("--accent"), colorToken("--accent-soft"));
  const ramChart = createChart(ramCanvas, colorToken("--green"), colorToken("--green-soft"));
  const cpuFallback = [];
  const ramFallback = [];

  function updateTheme() {
    if (cpuChart) {
      cpuChart.data.datasets[0].borderColor = colorToken("--accent");
      cpuChart.data.datasets[0].backgroundColor = colorToken("--accent-soft");
      cpuChart.update("none");
    }
    if (ramChart) {
      ramChart.data.datasets[0].borderColor = colorToken("--green");
      ramChart.data.datasets[0].backgroundColor = colorToken("--green-soft");
      ramChart.update("none");
    }
  }

  return {
    update(cpu, ram) {
      append(cpuChart, cpu, cpuFallback);
      append(ramChart, ram, ramFallback);
    },
    reset() {
      for (const chart of [cpuChart, ramChart]) {
        if (!chart) continue;
        chart.data.datasets[0].data = [];
        chart.update("none");
      }
      cpuFallback.length = 0;
      ramFallback.length = 0;
    },
    updateTheme,
    destroy() {
      cpuChart?.destroy();
      ramChart?.destroy();
    },
  };
}

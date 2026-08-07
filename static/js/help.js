// Help dialog controller. Context-aware: opens on the currently active workspace.
const dialog = document.getElementById("help-dialog");
const openButton = document.getElementById("help-open");
const guides = [...document.querySelectorAll("[data-help-workspace]")];
const navItems = [...document.querySelectorAll("[data-help-target]")];
const closeButtons = [...document.querySelectorAll("[data-help-close]")];
const focusableFallback = dialog?.querySelector(".help-nav-item");

function activeWorkspace() {
  const current = document.querySelector("[data-workspace].is-active");
  if (!current) return "overview";
  return current.dataset.workspace;
}

function showGuide(target) {
  const valid = guides.some((section) => section.dataset.helpWorkspace === target);
  const resolved = valid ? target : "overview";
  for (const section of guides) section.hidden = section.dataset.helpWorkspace !== resolved;
  for (const item of navItems) item.classList.toggle("is-active", item.dataset.helpTarget === resolved);
  return resolved;
}

export function openHelp({ focus = false } = {}) {
  if (!dialog || dialog.open) return;
  showGuide(activeWorkspace());
  dialog.showModal();
  if (focus) (dialog.querySelector(".help-nav-item.is-active") || focusableFallback)?.focus({ preventScroll: true });
}

export function closeHelp() {
  if (dialog?.open) dialog.close();
}

export function initHelp() {
  if (!dialog || !openButton) return;
  openButton.addEventListener("click", () => openHelp({ focus: true }));
  for (const button of closeButtons) button.addEventListener("click", closeHelp);
  for (const item of navItems) {
    item.addEventListener("click", () => showGuide(item.dataset.helpTarget));
  }
  dialog.addEventListener("cancel", (event) => { event.preventDefault(); closeHelp(); });
  dialog.addEventListener("click", (event) => {
    if (event.target === dialog) closeHelp(); // backdrop click
  });
}

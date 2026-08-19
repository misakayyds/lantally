async function loadView(name) {
  const target = document.querySelector(`[data-view="${name}"]`);
  if (!target) return;
  const response = await fetch(`/v1/${name}`, { credentials: "same-origin" });
  if (!response.ok) {
    target.textContent = `Login required for /v1/${name}`;
    return;
  }
  target.textContent = JSON.stringify(await response.json(), null, 2);
}

for (const view of ["overview", "devices", "nodes", "proxy", "alerts"]) {
  loadView(view);
}

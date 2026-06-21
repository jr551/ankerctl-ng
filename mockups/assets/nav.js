/* ankerctl-ng mockups · shared nav + theme bootstrap */

const PAGES = [
  { id: "dashboard", href: "index.html", label: "Dashboard", icon: "home" },
  { id: "slice",     href: "slice.html", label: "Slice & Build", icon: "box" },
  { id: "history",   href: "history.html", label: "History", icon: "clock" },
  { id: "filaments", href: "filaments.html", label: "Filaments", icon: "droplet" },
  { id: "setup",     href: "setup.html", label: "Settings", icon: "gear" }
];

const ICONS = {
  home: '<path d="M3 10.5 12 3l9 7.5"/><path d="M5 9.5V21h5v-6h4v6h5V9.5"/>',
  box: '<path d="M12 3 3 7.5v9L12 21l9-4.5v-9L12 3Z"/><path d="M3 7.5 12 12l9-4.5"/><path d="M12 12v9"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  droplet: '<path d="M12 3s6 6.5 6 11a6 6 0 0 1-12 0c0-4.5 6-11 6-11Z"/>',
  gear: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1Z"/>',
  magic: '<path d="M5 3v4M3 5h4M6 17v4M4 19h4M13 3l3.5 7.5L24 14l-7.5 3.5L13 25l-3.5-7.5L2 14l7.5-3.5L13 3Z" transform="scale(0.7) translate(6 0)"/>',
  printer: '<path d="M6 9V3h12v6"/><rect x="6" y="13" width="12" height="8" rx="1"/><path d="M6 17H4a2 2 0 0 1-2-2v-3a2 2 0 0 1 2-2h16a2 2 0 0 1 2 2v3a2 2 0 0 1-2 2h-2"/>',
  estop: '<rect x="3" y="3" width="18" height="18" rx="2"/><path d="M12 8v8M8 12h8"/>',
  bell: '<path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/><path d="M10 21a2 2 0 0 0 4 0"/>',
  camera: '<path d="M2 7a2 2 0 0 1 2-2h3l2-2h6l2 2h3a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V7Z"/><circle cx="12" cy="13" r="4"/>',
  bolt: '<path d="M13 2 4 14h7l-1 8 9-12h-7l1-8Z"/>',
  moon: '<path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8Z"/>',
  sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M2 12h2M20 12h2M5 5l1.5 1.5M17.5 17.5 19 19M19 5l-1.5 1.5M6.5 17.5 5 19"/>',
  chev: '<path d="m9 6 6 6-6 6"/>',
  plug: '<path d="M9 2v6M15 2v6M6 8h12v3a6 6 0 0 1-12 0V8Z"/><path d="M12 17v5"/>',
  fire: '<path d="M12 2s4 4 4 8a4 4 0 0 1-8 0c0-1 .5-2 1-2.5C9 9 12 8 12 2Z"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/>',
  sparkles: '<path d="M12 3l2 5 5 2-5 2-2 5-2-5-5-2 5-2 2-5Z"/><path d="M19 14l1 2 2 1-2 1-1 2-1-2-2-1 2-1 1-2Z"/>',
  // settings-section icons
  user: '<circle cx="12" cy="8" r="4"/><path d="M4 21a8 8 0 0 1 16 0"/>',
  wrench: '<path d="M14.7 6.3a4 4 0 0 0-5.4 5.4L3 18v3h3l6.3-6.3a4 4 0 0 0 5.4-5.4l-2.1 2.1-2.4-.6-.6-2.4Z"/>',
  palette: '<circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="4"/>',
  film: '<rect x="3" y="3" width="18" height="18" rx="2"/><path d="M3 9h18M9 21V9"/>',
  key: '<circle cx="8" cy="15" r="4"/><path d="m10.8 12.2 8.2-8.2M16 5l3 3M19 2l3 3"/>',
  home: '<path d="M3 10.5 12 3l9 7.5"/><path d="M5 9.5V21h5v-6h4v6h5V9.5"/>',
  globe: '<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18"/>',
  save: '<path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2Z"/><path d="M17 21v-8H7v8M7 3v5h8"/>',
  copy: '<rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>',
  refresh: '<path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5"/>',
  trash: '<path d="M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  upload: '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><path d="m7 10 5-5 5 5"/><path d="M12 5v12"/>',
  check: '<path d="M5 12l4 4L19 6"/>',
  shield: '<path d="M12 2 4 5v6c0 5 3.5 8 8 9 4.5-1 8-4 8-9V5Z"/>',
  disk: '<circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="2"/>',
  power: '<path d="M18.36 6.64A9 9 0 1 1 5.64 6.64"/><path d="M12 2v10"/>'
};

const SETTINGS_SECTIONS = [
  { id: "account",       href: "setup-account.html",       label: "Account",         icon: "user" },
  { id: "printer",       href: "setup-printer.html",       label: "Printer",         icon: "printer" },
  { id: "tools",         href: "setup-tools.html",         label: "Tools",           icon: "wrench" },
  { id: "notifications", href: "setup-notifications.html", label: "Notifications",   icon: "bell" },
  { id: "appearance",    href: "setup.html",               label: "Appearance",      icon: "palette" },
  { id: "camera-ai",     href: "setup-camera-ai.html",     label: "Camera & AI",     icon: "camera" },
  { id: "timelapse",     href: "setup-timelapse.html",     label: "Timelapse",       icon: "film" },
  { id: "mqtt",          href: "setup-mqtt.html",          label: "MQTT / API",      icon: "key" },
  { id: "power",         href: "setup-power.html",         label: "Power & Socket",  icon: "power" },
  { id: "homeassistant", href: "setup-homeassistant.html", label: "Home Assistant",  icon: "home" }
];

function icon(name, cls = "") {
  return `<svg class="ic ${cls}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ICONS[name] || ""}</svg>`;
}

function buildSidebar(active) {
  const items = PAGES.map(p => `
    <a class="nav-item ${p.id === active ? "active" : ""}" href="${p.href}">
      ${icon(p.icon)}
      <span>${p.label}</span>
    </a>`).join("");
  return `
    <aside class="sidebar">
      <div class="brand">
        <div class="brand-mark">A</div>
        <div>
          <div class="brand-name">ankerctl<span class="ng">-ng</span></div>
          <div class="brand-tag">extra AI goodness</div>
        </div>
      </div>

      <div class="printer-select" title="Switch printer">
        <span class="led"></span>
        <div class="meta">
          <div class="name">Office M5C</div>
          <div class="sub">…QXY12</div>
        </div>
        <span class="caret">${icon("chev")}</span>
      </div>

      <div class="nav-section">Workspace</div>
      ${items}

      <a class="nav-item beginner" href="beginner.html">
        ${icon("magic")}
        <span>Beginner Mode</span>
      </a>

      <div class="nav-section">System</div>
      <a class="nav-item" href="setup.html">${icon("bell")}<span>Notifications</span><span class="badge">3</span></a>
      <a class="nav-item" href="setup.html">${icon("camera")}<span>Camera &amp; AI</span></a>

      <div class="sidebar-footer">
        <div class="row"><span>v1.1.1</span><span class="mono">42°C</span></div>
        <div class="row"><span>uptime</span><span class="mono">2h 14m</span></div>
      </div>
    </aside>`;
}

function buildTopbar(title, active) {
  return `
    <header class="topbar">
      <div>
        <div class="crumb">ankerctl-ng / ${title}</div>
        <h1>${title}</h1>
      </div>
      <div class="spacer"></div>
      <div class="conn-pills">
        <span class="pill ok"><span class="led"></span>MQTT</span>
        <span class="pill ok"><span class="led"></span>PPPP</span>
        <span class="pill ok"><span class="led"></span>CAMERA</span>
        <span class="pill off"><span class="led"></span>CTRL</span>
      </div>
      <button class="icon-btn" id="theme-toggle" title="Toggle theme">${icon("moon")}</button>
      <button class="estop">${icon("estop")} E-STOP</button>
    </header>`;
}

function buildSubnav(active) {
  const items = SETTINGS_SECTIONS.map(s => `
    <a class="${s.id === active ? "active" : ""}" href="${s.href}">
      ${icon(s.icon)}
      <span>${s.label}</span>
    </a>`).join("");
  return `<aside class="subnav">${items}</aside>`;
}

function buildBanner(current) {
  const links = PAGES.map(p =>
    `<a href="${p.href}"${p.id === current ? ' style="opacity:.6"' : ''}>${p.label}</a>`
  ).join('<span class="sep">·</span>');
  return `
    <div class="mockup-banner">
      <span class="dot"></span>
      <strong>UI REDESIGN MOCKUP</strong>
      <span class="sep">·</span>
      <span>static preview — not wired to live data</span>
      <span class="sep">·</span>
      ${links}
      <span class="sep">·</span>
      <a href="beginner.html">Beginner Mode</a>
      <span class="sep">·</span>
      <a href="README.md">README</a>
    </div>`;
}

function init(options = {}) {
  const root = document.getElementById("app") || document.body;
  const theme = localStorage.getItem("ank-ng-theme") || "dark";
  document.documentElement.setAttribute("data-theme", theme);

  // Inject shell
  const banner = buildBanner(options.active);
  const sidebar = buildSidebar(options.active);
  const topbar = buildTopbar(options.title, options.active);
  const mainInner = root.innerHTML;
  root.innerHTML = `
    ${banner}
    <div class="app">
      ${sidebar}
      <div class="main">
        ${topbar}
        <main class="content">${mainInner}</main>
      </div>
    </div>`;

  // Theme toggle
  const tBtn = document.getElementById("theme-toggle");
  tBtn.addEventListener("click", () => {
    const cur = document.documentElement.getAttribute("data-theme");
    const next = cur === "dark" ? "light" : "dark";
    document.documentElement.setAttribute("data-theme", next);
    localStorage.setItem("ank-ng-theme", next);
    tBtn.innerHTML = icon(next === "dark" ? "moon" : "sun");
  });
  tBtn.innerHTML = icon(theme === "dark" ? "moon" : "sun");

  // Let pages tweak the injected shell (e.g. Basic Mode collapses the sidebar)
  if (typeof options.afterInject === "function") {
    options.afterInject();
  }

  // Settings sub-nav: fill any #subnav-slot element on the page
  if (options.subnav) {
    const slots = document.querySelectorAll("#subnav-slot");
    slots.forEach((slot) => {
      slot.outerHTML = buildSubnav(options.subnav);
    });
  }
}

document.addEventListener("DOMContentLoaded", () => {
  // expose helpers globally for inline pages that want them
  window.__ngIcon = icon;
});

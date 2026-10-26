const actions = [
  ["fleet", "Mostrar a frota", "Espalha os carros de novo, perto da praça, e manda heartbeat."],
  ["success", "Sucesso no grafo", "O Dijkstra escolhe o menor tempo de rua entre os 10 mais próximos."],
  ["race", "Dois pedidos juntos", "Dois solicitantes na praça. Cada um fica com um carro."],
  ["routing-down", "Cair o pod do routing", "Escala o deployment para 0. A corrida usa a linha reta."],
  ["routing-up", "Religar o routing", "Recria o HPA e espera 2 pods."],
  ["lost", "Motorista some", "Para o heartbeat. Em 30 s a corrida troca de carro."],
  ["nobody", "Ninguém por perto", "Embarque no Rio. A frota fica em Belo Horizonte."],
  ["cancel", "Cancelar a corrida", "Atribui, o carro anda, e o cancelamento devolve o motorista."],
  ["reset", "Voltar ao padrão", "Embarque na praça e os carros no lugar da abertura."],
];

const palette = ["#3ddec8", "#f0b429", "#8eb6ff", "#d7b4ff", "#ff8a7a", "#9be36a", "#f28ad6", "#7ad7f0", "#e6d36a", "#c8b4ff", "#ffa15a", "#6ee7b7", "#f0a3c2", "#a3c4f0", "#d4f08a", "#ffd27a", "#b4f0e6", "#e0b4ff", "#f0c8a3", "#9ad0ff"];

function colorOf(role) {
  const n = Number(String(role || "").replace(/\D/g, ""));
  if (!n) return palette[0];
  return palette[(n - 1) % palette.length];
}

const NS = "http://www.w3.org/2000/svg";
const svg = document.getElementById("map");
const story = document.getElementById("story");
const logEl = document.getElementById("log");

let mapData = null;
let state = { drivers: [], rides: [], trips: [], log: [], cluster: {}, connected: false, busy: "" };
let skew = 0;

function el(name, attrs, text) {
  const node = document.createElementNS(NS, name);
  for (const [key, value] of Object.entries(attrs || {})) node.setAttribute(key, value);
  if (text) node.textContent = text;
  return node;
}

function project(lat, lng) {
  return [
    mapData.x0 + (lng - mapData.min_lng) * mapData.x_scale,
    mapData.y0 + (mapData.max_lat - lat) * mapData.y_scale,
  ];
}

function drawMap() {
  svg.setAttribute("viewBox", `0 0 ${mapData.width} ${mapData.height}`);
  svg.classList.toggle("dense", mapData.edges.length > 80);
  svg.replaceChildren();
  svg.append(el("g", { id: "edges" }));
  svg.append(el("g", { id: "trips" }));
  svg.append(el("g", { id: "nodes" }));
  svg.append(el("g", { id: "passengers" }));
  svg.append(el("g", { id: "cars" }));

  const byId = new Map(mapData.nodes.map((node) => [node.id, node]));
  const edges = svg.querySelector("#edges");
  const seen = new Set();
  for (const edge of mapData.edges) {
    const key = edge.from < edge.to ? `${edge.from}:${edge.to}` : `${edge.to}:${edge.from}`;
    if (seen.has(key)) continue;
    seen.add(key);
    const from = byId.get(edge.from);
    const to = byId.get(edge.to);
    if (!from || !to) continue;
    const [x1, y1] = project(from.lat, from.lng);
    const [x2, y2] = project(to.lat, to.lng);
    edges.append(el("line", { x1, y1, x2, y2, class: "road" }));
  }

  const nodes = svg.querySelector("#nodes");
  const drawn = mapData.nodes.length > 80 ? mapData.nodes.filter((node) => node.kind === "pickup") : mapData.nodes;
  for (const node of drawn) {
    const [x, y] = project(node.lat, node.lng);
    nodes.append(el("circle", { cx: x, cy: y, r: node.kind === "pickup" ? 7 : 3.5, class: `node ${node.kind}` }));
  }
  const [px, py] = project(mapData.pickup.lat, mapData.pickup.lng);
  nodes.append(el("text", { x: px + 12, y: py + 4, class: "map-label" }, mapData.pickup.label || "embarque"));

  const radius = Math.round(mapData.fleet_radius_m);
  const place = mapData.pickup.label === "ponto clicado" ? "Embarque no ponto clicado" : `Embarque na ${mapData.pickup.label}`;
  story.textContent = `${place}. ${mapData.compare.length} carros em pontos aleatórios, até ${radius} m. O matching pede o ETA dos ${mapData.match_k} mais próximos em linha reta. No grafo, o Dijkstra fica com o menor tempo de rua. Com o routing em zero, o Haversine fica com o mais perto. Clique no mapa para mudar o embarque.`;
  renderCompare();
}

function rowKey(row) {
  return row.id || row.role || row.name;
}

function renderCompare() {
  if (!mapData) return;
  const body = document.querySelector("#compare tbody");
  const source = state.drivers && state.drivers.length ? state.drivers : mapData.compare;
  const rows = source ? [...source] : [];
  body.replaceChildren();
  if (rows.length === 0) return;
  const ranked = rows.sort((a, b) => a.haversine_m - b.haversine_m || String(rowKey(a)).localeCompare(String(rowKey(b))));
  const pool = ranked.filter((row) => row.haversine_m <= mapData.match_radius_m).slice(0, mapData.match_k);
  const poolKeys = new Set(pool.map(rowKey));
  const better = (left, right, time, meters) => time(left) - time(right) || meters(left) - meters(right) || String(rowKey(left)).localeCompare(String(rowKey(right)));
  const pick = (score) => pool.reduce((best, row) => (better(row, best, score.time, score.meters) < 0 ? row : best));
  const streetTime = (row) => (row.dijkstra_s >= 1000000000 ? Infinity : row.dijkstra_s);
  const bestD = pool.some((row) => streetTime(row) < Infinity) ? rowKey(pick({ time: streetTime, meters: (row) => row.dijkstra_m })) : "";
  const bestH = pool.length ? rowKey(pick({ time: (row) => row.haversine_s, meters: (row) => row.haversine_m })) : "";
  for (const row of ranked) {
    const tr = document.createElement("tr");
    const key = rowKey(row);
    if (!poolKeys.has(key)) tr.className = "out";
    const street = streetTime(row) === Infinity ? "sem rua" : `${row.dijkstra_s} s · ${row.dijkstra_m} m`;
    tr.innerHTML = `<td>${row.name}</td><td class="${key === bestD ? "best-d" : ""}">${street}</td><td class="${key === bestH ? "best-h" : ""}">${row.haversine_s} s · ${row.haversine_m} m</td>`;
    body.append(tr);
  }
}

function serverNow() {
  return Date.now() + skew;
}

function along(path, frac) {
  const points = path.map((point) => project(point.lat, point.lng));
  if (points.length === 0) return null;
  const angleOf = (a, b) => Math.atan2(b[1] - a[1], b[0] - a[0]) * 180 / Math.PI;
  if (points.length === 1 || frac <= 0) {
    const next = points[1] || points[0];
    return { x: points[0][0], y: points[0][1], angle: angleOf(points[0], next) };
  }
  if (frac >= 1) {
    const prev = points[points.length - 2];
    const last = points[points.length - 1];
    return { x: last[0], y: last[1], angle: angleOf(prev, last) };
  }
  const lengths = [];
  let total = 0;
  for (let i = 0; i < points.length - 1; i++) {
    const len = Math.hypot(points[i + 1][0] - points[i][0], points[i + 1][1] - points[i][1]);
    lengths.push(len);
    total += len;
  }
  let left = frac * total;
  for (let i = 0; i < lengths.length; i++) {
    if (lengths[i] === 0) continue;
    if (left <= lengths[i]) {
      const t = left / lengths[i];
      return {
        x: points[i][0] + (points[i + 1][0] - points[i][0]) * t,
        y: points[i][1] + (points[i + 1][1] - points[i][1]) * t,
        angle: angleOf(points[i], points[i + 1]),
      };
    }
    left -= lengths[i];
  }
  const last = points[points.length - 1];
  return { x: last[0], y: last[1], angle: 0 };
}

function paintPassengers() {
  const layer = svg.querySelector("#passengers");
  if (!layer || !mapData) return;
  layer.replaceChildren();
  const live = (state.rides || []).filter((ride) => {
    return ride.status !== "CANCELLED" && ride.status !== "NO_DRIVER_AVAILABLE" && Number.isFinite(ride.pickup_lat) && Number.isFinite(ride.pickup_lng);
  });
  const groups = new Map();
  for (const ride of live) {
    const key = `${ride.pickup_lat.toFixed(5)},${ride.pickup_lng.toFixed(5)}`;
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(ride);
  }
  for (const rides of groups.values()) {
    rides.sort((a, b) => String(a.label || "").localeCompare(String(b.label || ""), "pt-BR"));
    const [px, py] = project(rides[0].pickup_lat, rides[0].pickup_lng);
    const gap = 96;
    const origin = -((rides.length - 1) * gap) / 2;
    for (let i = 0; i < rides.length; i++) {
      const ride = rides[i];
      const driver = (state.drivers || []).find((item) => item.id === ride.driver_id);
      const color = driver ? colorOf(driver.role) : "#f4f7ef";
      const x = px + origin + i * gap;
      const y = py - 30;
      layer.append(el("line", { x1: px, y1: py, x2: x, y2: y + 12, class: "passenger-stem", stroke: color }));
      const figure = el("g", { transform: `translate(${x} ${y})`, class: "passenger" });
      figure.append(el("circle", { r: "12", class: "passenger-ring", stroke: color }));
      figure.append(el("circle", { cy: "-3.4", r: "3.2", fill: color }));
      figure.append(el("path", { d: "M-6.2,7.4 Q0,1.4 6.2,7.4", fill: "none", stroke: color, "stroke-width": "1.8", "stroke-linecap": "round" }));
      figure.append(el("text", { y: "-18", "text-anchor": "middle", class: "passenger-name" }, ride.label || "Solicitante"));
      layer.append(figure);
    }
  }
}

function paintCars() {
  if (!mapData) return;
  const trips = svg.querySelector("#trips");
  const cars = svg.querySelector("#cars");
  if (!trips || !cars) return;
  trips.replaceChildren();
  cars.replaceChildren();
  const now = serverNow();
  for (const trip of state.trips || []) {
    const driver = (state.drivers || []).find((item) => item.id === trip.driver_id);
    const color = colorOf(driver?.role);
    const points = trip.path.map((point) => project(point.lat, point.lng).join(",")).join(" ");
    trips.append(el("polyline", { points, class: "trip-line", stroke: color }));
  }
  for (const driver of state.drivers || []) {
    const trip = (state.trips || []).find((item) => item.driver_id === driver.id);
    let x;
    let y;
    let angle = -90;
    if (trip && trip.path?.length) {
      const dur = trip.duration_ms || 1;
      const frac = Math.min(1, Math.max(0, (now - Date.parse(trip.started)) / dur));
      const pos = along(trip.path, frac);
      x = pos.x;
      y = pos.y;
      angle = pos.angle;
    } else {
      [x, y] = project(driver.lat, driver.lng);
    }
    const compact = (state.drivers || []).length > 8;
    const color = colorOf(driver.role);
    const group = el("g", { transform: `translate(${x} ${y})` });
    if (driver.frozen) {
      group.append(el("circle", { r: "16", fill: "none", stroke: "#ff6b6b", "stroke-dasharray": "3 3" }));
    }
    const body = el("g", { transform: `rotate(${angle}) scale(${compact ? 0.72 : 1})` });
    body.append(el("path", { d: "M-12,-6 H6 L13,0 L6,6 H-12 Z", fill: color }));
    group.append(body);
    if (!compact || driver.frozen || trip) {
      const caption = driver.frozen && driver.lost_in_s > 0 ? `${driver.name} · ${driver.lost_in_s}s` : driver.name;
      group.append(el("text", { y: "-16", "text-anchor": "middle", class: "car-name" }, caption));
    }
    cars.append(group);
  }
  requestAnimationFrame(paintCars);
}

function renderChrome() {
  svg.classList.toggle("busy", Boolean(state.busy));
  const backends = document.getElementById("backends");
  backends.textContent = state.connected ? "ride e driver no ar" : "ride e driver sem conexão";
  backends.className = `chip ${state.connected ? "ok" : "bad"}`;

  const cluster = document.getElementById("cluster");
  const info = state.cluster || {};
  if (!info.known) {
    cluster.textContent = "consultando o routing";
    cluster.className = "chip";
  } else if (info.error) {
    cluster.textContent = info.error;
    cluster.className = "chip bad";
  } else if (info.ready === 0) {
    cluster.textContent = info.held_down ? "routing em 0 · derrubado aqui" : "routing em 0";
    cluster.className = "chip bad";
  } else if (info.ready >= 2 && info.hpa && info.endpoints > 0) {
    cluster.textContent = `routing · ${info.ready} pods · HPA`;
    cluster.className = "chip ok";
  } else {
    cluster.textContent = `routing · ${info.ready} pods`;
    cluster.className = "chip warn";
  }

  for (const button of document.querySelectorAll("#actions button")) {
    const needsAPI = button.dataset.scenario !== "routing-up" && button.dataset.scenario !== "reset";
    button.disabled = Boolean(state.busy) || (needsAPI && !state.connected);
    button.classList.toggle("active", state.busy === button.dataset.scenario);
  }

  const rides = document.getElementById("rides");
  const items = state.rides || [];
  if (items.length === 0) {
    rides.innerHTML = `<div class="empty">Nenhuma corrida ainda.</div>`;
  } else {
    rides.replaceChildren();
    for (const ride of [...items].reverse()) {
      const card = document.createElement("article");
      card.className = "ride";
      const who = ride.driver_name ? `${ride.driver_name} · ${ride.eta_s} s · ${ride.distance_m} m` : ride.status;
      const engine = ride.eta_engine
        ? `<span class="engine ${ride.eta_engine.startsWith("dijkstra") ? "dijkstra" : "haversine"}">${ride.eta_engine}</span>`
        : "";
      card.innerHTML = `<b>${ride.label || "Corrida"}</b><p>${who}</p>${engine}`;
      rides.append(card);
    }
  }

  const nearBottom = logEl.scrollHeight - logEl.scrollTop - logEl.clientHeight < 24;
  logEl.replaceChildren();
  for (const line of state.log || []) {
    const item = document.createElement("li");
    const stamp = new Date(line.at).toLocaleTimeString("pt-BR", { hour12: false });
    item.textContent = `${stamp}  ${line.text}`;
    if (String(line.text).startsWith("Falhou")) item.className = "fail";
    logEl.append(item);
  }
  if (nearBottom) logEl.scrollTop = logEl.scrollHeight;
  renderCompare();
  syncPickup();
  paintPassengers();
}

function syncPickup() {
  const pickup = state.pickup;
  if (!mapData || !pickup || pickup.node_id === mapData.pickup.node_id) return;
  mapData.pickup = pickup;
  for (const node of mapData.nodes) {
    if (node.id === pickup.node_id) node.kind = "pickup";
    else if (node.kind === "pickup") node.kind = "road";
  }
  drawMap();
}

function buildActions() {
  const root = document.getElementById("actions");
  for (const [name, title, detail] of actions) {
    const button = document.createElement("button");
    button.type = "button";
    button.dataset.scenario = name;
    button.innerHTML = `<strong>${title}</strong><span>${detail}</span>`;
    button.addEventListener("click", () => runScenario(name, button));
    root.append(button);
  }
}

async function runScenario(name, button) {
  button.disabled = true;
  try {
    const response = await fetch(`/api/scenarios/${name}`, { method: "POST" });
    if (!response.ok) {
      const body = await response.json().catch(() => ({}));
      state.log = state.log.concat([{ at: new Date().toISOString(), text: body.error || `HTTP ${response.status}` }]);
      renderChrome();
    }
  } catch (err) {
    state.log = state.log.concat([{ at: new Date().toISOString(), text: String(err) }]);
    renderChrome();
  }
}

async function poll() {
  try {
    const response = await fetch("/api/state");
    if (response.ok) {
      state = await response.json();
      if (state.now) skew = Date.parse(state.now) - Date.now();
      renderChrome();
    }
  } catch {
    state.connected = false;
    renderChrome();
  }
}

function svgPoint(event) {
  const ctm = svg.getScreenCTM();
  if (!ctm) return null;
  const point = svg.createSVGPoint();
  point.x = event.clientX;
  point.y = event.clientY;
  const local = point.matrixTransform(ctm.inverse());
  return { x: local.x, y: local.y };
}

function nearestNode(point) {
  let best = null;
  let bestD = Infinity;
  for (const node of mapData.nodes) {
    const [x, y] = project(node.lat, node.lng);
    const dist = Math.hypot(x - point.x, y - point.y);
    if (dist < bestD) {
      bestD = dist;
      best = node;
    }
  }
  return best;
}

async function choosePickup(nodeId) {
  const response = await fetch("/api/pickup", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ node_id: nodeId }),
  });
  const body = await response.json().catch(() => ({}));
  if (!response.ok) {
    state.log = state.log.concat([{ at: new Date().toISOString(), text: body.error || `HTTP ${response.status}` }]);
    renderChrome();
    return;
  }
  mapData = body;
  if (state.drivers?.length && mapData.compare) {
    const byRole = new Map(mapData.compare.map((row) => [row.role, row]));
    for (const driver of state.drivers) {
      const row = byRole.get(driver.role);
      if (!row) continue;
      driver.dijkstra_s = row.dijkstra_s;
      driver.dijkstra_m = row.dijkstra_m;
      driver.haversine_s = row.haversine_s;
      driver.haversine_m = row.haversine_m;
    }
  }
  drawMap();
  renderChrome();
}

function onMapClick(event) {
  if (!mapData || state.busy) return;
  if (event.target.closest && event.target.closest("#cars, #passengers")) return;
  const point = svgPoint(event);
  if (!point) return;
  const node = nearestNode(point);
  if (!node || node.id === mapData.pickup.node_id) return;
  choosePickup(node.id);
}

async function boot() {
  buildActions();
  const response = await fetch("/api/map");
  if (!response.ok) {
    story.textContent = "Não consegui ler o mapa.";
    return;
  }
  mapData = await response.json();
  drawMap();
  svg.addEventListener("click", onMapClick);
  requestAnimationFrame(paintCars);
  await poll();
  setInterval(poll, 400);
}

boot();

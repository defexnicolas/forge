package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"forge/internal/yarn"
)

type yarnGraphNode struct {
	ID        string   `json:"id"`
	Label     string   `json:"label"`
	Kind      string   `json:"kind"`
	Path      string   `json:"path,omitempty"`
	Summary   string   `json:"summary,omitempty"`
	Content   string   `json:"content"`
	Links     []string `json:"links,omitempty"`
	UpdatedAt string   `json:"updated_at"`
}

type yarnGraphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

type yarnGraphPayload struct {
	GeneratedAt string          `json:"generated_at"`
	NodeCount   int             `json:"node_count"`
	EdgeCount   int             `json:"edge_count"`
	Nodes       []yarnGraphNode `json:"nodes"`
	Edges       []yarnGraphEdge `json:"edges"`
}

var openYarnGraphPath = defaultOpenYarnGraphPath

func writeYarnGraphHTML(cwd string, nodes []yarn.Node) (string, error) {
	outDir := filepath.Join(cwd, ".forge", "yarn")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	html, err := buildYarnGraphHTML(nodes)
	if err != nil {
		return "", err
	}
	path := filepath.Join(outDir, "graph.html")
	if err := os.WriteFile(path, []byte(html), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func buildYarnGraphHTML(nodes []yarn.Node) (string, error) {
	payload := yarnGraphPayload{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Nodes:       make([]yarnGraphNode, 0, len(nodes)),
	}
	for _, node := range nodes {
		label := strings.TrimSpace(node.Summary)
		if label == "" {
			label = node.ID
		}
		payload.Nodes = append(payload.Nodes, yarnGraphNode{
			ID:        node.ID,
			Label:     label,
			Kind:      node.Kind,
			Path:      node.Path,
			Summary:   node.Summary,
			Content:   node.Content,
			Links:     append([]string(nil), node.Links...),
			UpdatedAt: node.UpdatedAt.Format(time.RFC3339),
		})
		for _, link := range node.Links {
			payload.Edges = append(payload.Edges, yarnGraphEdge{Source: node.ID, Target: link})
		}
	}
	payload.NodeCount = len(payload.Nodes)
	payload.EdgeCount = len(payload.Edges)
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(yarnGraphHTMLTemplate, string(data)), nil
}

func defaultOpenYarnGraphPath(path string) error {
	return openInDefaultApp(path)
}

const yarnGraphHTMLTemplate = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Forge YARN Graph</title>
<style>
  :root {
    color-scheme: dark;
    --bg: #091019;
    --panel: #0f1722;
    --panel-border: #233245;
    --ink: #d7e2ee;
    --muted: #8da0b3;
    --accent: #7fd1b9;
    --danger: #ff8f8f;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0;
    background:
      radial-gradient(circle at top, rgba(53, 118, 167, 0.20), transparent 34%%),
      linear-gradient(180deg, #0a1018 0%%, #06090f 100%%);
    color: var(--ink);
    font: 14px/1.45 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  }
  .layout {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 360px;
    height: 100vh;
  }
  .graph-shell {
    position: relative;
    border-right: 1px solid rgba(127, 209, 185, 0.14);
    overflow: hidden;
    min-height: 420px;
  }
  canvas {
    display: block;
    width: 100%%;
    height: 100%%;
    cursor: grab;
    background:
      radial-gradient(circle at 20%% 20%%, rgba(127, 209, 185, 0.06), transparent 32%%),
      linear-gradient(180deg, rgba(6, 10, 15, 0.9), rgba(6, 10, 15, 1));
  }
  canvas.dragging { cursor: grabbing; }
  .overlay {
    position: absolute;
    top: 16px;
    left: 16px;
    display: flex;
    gap: 12px;
    flex-wrap: wrap;
    padding: 12px 14px;
    background: rgba(10, 16, 24, 0.72);
    border: 1px solid rgba(127, 209, 185, 0.18);
    backdrop-filter: blur(12px);
    border-radius: 12px;
    box-shadow: 0 18px 40px rgba(0, 0, 0, 0.25);
  }
  .overlay b { color: var(--accent); display: block; margin-bottom: 2px; }
  .overlay span { color: var(--muted); }
  .panel {
    min-width: 0;
    background: linear-gradient(180deg, rgba(15, 23, 34, 0.94), rgba(9, 14, 21, 0.98));
    padding: 18px;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .panel h1 {
    margin: 0;
    font-size: 18px;
    letter-spacing: 0.02em;
  }
  .panel .hint {
    color: var(--muted);
    margin-top: -4px;
  }
  .meta {
    display: grid;
    grid-template-columns: 88px minmax(0, 1fr);
    gap: 8px 10px;
    align-items: start;
  }
  .meta dt {
    color: var(--muted);
  }
  .meta dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .links {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .chip {
    padding: 4px 8px;
    border-radius: 999px;
    background: rgba(127, 209, 185, 0.10);
    border: 1px solid rgba(127, 209, 185, 0.15);
    color: #c2eee1;
    font-size: 12px;
  }
  .content-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 10px;
  }
  button {
    appearance: none;
    border: 1px solid rgba(127, 209, 185, 0.22);
    background: rgba(127, 209, 185, 0.08);
    color: var(--ink);
    border-radius: 8px;
    padding: 7px 10px;
    cursor: pointer;
    font: inherit;
  }
  button:hover { border-color: rgba(127, 209, 185, 0.40); }
  pre {
    margin: 0;
    padding: 12px;
    border-radius: 12px;
    background: rgba(0, 0, 0, 0.22);
    border: 1px solid rgba(255, 255, 255, 0.06);
    overflow: auto;
    white-space: pre-wrap;
    word-break: break-word;
    max-height: calc(100vh - 320px);
  }
  .empty {
    color: var(--muted);
    padding: 16px;
    border-radius: 12px;
    background: rgba(255, 255, 255, 0.03);
    border: 1px dashed rgba(255, 255, 255, 0.08);
  }
  @media (max-width: 980px) {
    .layout { grid-template-columns: 1fr; grid-template-rows: 58vh auto; }
    .graph-shell { border-right: 0; border-bottom: 1px solid rgba(127, 209, 185, 0.14); }
    pre { max-height: 280px; }
  }
</style>
</head>
<body>
<div class="layout">
  <section class="graph-shell">
    <canvas id="graph"></canvas>
    <div class="overlay">
      <div><b>Generated</b><span id="generatedAt"></span></div>
      <div><b>Nodes</b><span id="nodeCount"></span></div>
      <div><b>Edges</b><span id="edgeCount"></span></div>
      <div><b>Controls</b><span>drag nodes, pan background, wheel zoom</span></div>
    </div>
  </section>
  <aside class="panel">
    <div>
      <h1>YARN Graph</h1>
      <div class="hint">Interactive local graph view for current YARN nodes.</div>
    </div>
    <div id="details" class="empty">Click a node to inspect its details.</div>
  </aside>
</div>
<script>
const payload = %s;
document.getElementById("generatedAt").textContent = payload.generated_at;
document.getElementById("nodeCount").textContent = String(payload.node_count);
document.getElementById("edgeCount").textContent = String(payload.edge_count);

const canvas = document.getElementById("graph");
const ctx = canvas.getContext("2d");
const details = document.getElementById("details");
const dpr = window.devicePixelRatio || 1;
const palette = ["#7fd1b9", "#8bc5ff", "#ffbd80", "#f89cd4", "#e6de8f", "#b9a6ff", "#7ce2ff", "#ff9d9d"];
const kindColors = new Map();
const colorForKind = (kind) => {
  if (!kindColors.has(kind)) {
    kindColors.set(kind, palette[kindColors.size %% palette.length]);
  }
  return kindColors.get(kind);
};
const nodes = payload.nodes.map((node, index) => {
  const angle = (Math.PI * 2 * index) / Math.max(payload.nodes.length, 1);
  const radius = 120 + (index %% 7) * 16;
  return {
    ...node,
    x: Math.cos(angle) * radius,
    y: Math.sin(angle) * radius,
    vx: 0,
    vy: 0,
    radius: 12,
    color: colorForKind(node.kind),
    fixed: false
  };
});
const nodeByID = new Map(nodes.map((node) => [node.id, node]));
const edges = payload.edges
  .map((edge) => ({ source: nodeByID.get(edge.source), target: nodeByID.get(edge.target) }))
  .filter((edge) => edge.source && edge.target);

let width = 0;
let height = 0;
let scale = 1;
let offsetX = 0;
let offsetY = 0;
let selectedID = null;
let dragNode = null;
let draggingCanvas = false;
let dragMoved = false;
let lastPointer = { x: 0, y: 0 };

function resize() {
  const parent = canvas.parentElement;
  width = canvas.clientWidth || (parent ? parent.clientWidth : 0) || window.innerWidth;
  height = canvas.clientHeight || (parent ? parent.clientHeight : 0) || Math.max(320, window.innerHeight - 120);
  canvas.width = Math.floor(width * dpr);
  canvas.height = Math.floor(height * dpr);
  if (ctx) {
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  }
  if (!offsetX && !offsetY) {
    offsetX = width / 2;
    offsetY = height / 2;
  }
}

function worldFromScreen(x, y) {
  return {
    x: (x - offsetX) / scale,
    y: (y - offsetY) / scale
  };
}

function screenFromWorld(x, y) {
  return {
    x: x * scale + offsetX,
    y: y * scale + offsetY
  };
}

function truncateText(text, limit) {
  if (!text) return "";
  if (text.length <= limit) return text;
  return text.slice(0, limit) + "\n[truncated]";
}

function escapeHTML(text) {
  return (text || "").replace(/[&<>"]/g, (char) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    "\"": "&quot;"
  })[char]);
}

function renderDetails(node) {
  if (!node) {
    details.className = "";
    const items = payload.nodes.length
      ? payload.nodes.map((item) =>
          '<div class="chip" style="display:block;margin-bottom:8px;">' +
          escapeHTML(item.kind) + ' · ' + escapeHTML(item.label || item.id) +
          '</div>').join("")
      : '<div class="empty">No YARN nodes available.</div>';
    details.innerHTML = [
      '<div>',
      '  <strong style="font-size:16px;">YARN Nodes</strong>',
      '  <div class="hint">Click a node in the graph or inspect the cached list below.</div>',
      '</div>',
      items
    ].join('');
    return;
  }
  details.className = "";
  const links = node.links && node.links.length
    ? '<div class="links">' + node.links.map((link) => '<span class="chip">' + escapeHTML(link) + '</span>').join("") + '</div>'
    : '<span class="hint">none</span>';
  details.innerHTML = [
    '<div>',
    '  <strong style="font-size:16px;">' + escapeHTML(node.label) + '</strong>',
    '  <div class="hint">' + escapeHTML(node.id) + '</div>',
    '</div>',
    '<dl class="meta">',
    '  <dt>Kind</dt><dd>' + escapeHTML(node.kind) + '</dd>',
    '  <dt>Path</dt><dd>' + (node.path ? escapeHTML(node.path) : '<span class="hint">none</span>') + '</dd>',
    '  <dt>Summary</dt><dd>' + (node.summary ? escapeHTML(node.summary) : '<span class="hint">none</span>') + '</dd>',
    '  <dt>Updated</dt><dd>' + escapeHTML(node.updated_at) + '</dd>',
    '  <dt>Links</dt><dd>' + links + '</dd>',
    '</dl>',
    '<div class="content-head">',
    '  <strong>Content</strong>',
    '  <button id="copyNodeContent" type="button">copy</button>',
    '</div>',
    '<pre>' + escapeHTML(truncateText(node.content, 12000)) + '</pre>'
  ].join('');
  const copyBtn = document.getElementById("copyNodeContent");
  copyBtn.onclick = async () => {
    try {
      await navigator.clipboard.writeText(node.content || "");
      copyBtn.textContent = "copied";
      setTimeout(() => { copyBtn.textContent = "copy"; }, 1200);
    } catch (_) {
      copyBtn.textContent = "copy failed";
      setTimeout(() => { copyBtn.textContent = "copy"; }, 1200);
    }
  };
}

function nodeAtScreen(x, y) {
  for (let i = nodes.length - 1; i >= 0; i--) {
    const node = nodes[i];
    const screen = screenFromWorld(node.x, node.y);
    const dx = x - screen.x;
    const dy = y - screen.y;
    const radius = node.radius * Math.max(1, scale * 0.9);
    if ((dx * dx) + (dy * dy) <= radius * radius) {
      return node;
    }
  }
  return null;
}

function pointerPosition(event) {
  const rect = canvas.getBoundingClientRect();
  return { x: event.clientX - rect.left, y: event.clientY - rect.top };
}

function applyForces() {
  const repulsion = 9000;
  const spring = 0.018;
  const springLength = 120;

  for (let i = 0; i < nodes.length; i++) {
    for (let j = i + 1; j < nodes.length; j++) {
      const a = nodes[i];
      const b = nodes[j];
      let dx = b.x - a.x;
      let dy = b.y - a.y;
      let distanceSq = dx * dx + dy * dy;
      if (distanceSq < 0.01) {
        dx = 0.1;
        dy = 0.1;
        distanceSq = 0.02;
      }
      const distance = Math.sqrt(distanceSq);
      const force = repulsion / distanceSq;
      const fx = (force * dx) / distance;
      const fy = (force * dy) / distance;
      if (!a.fixed) {
        a.vx -= fx;
        a.vy -= fy;
      }
      if (!b.fixed) {
        b.vx += fx;
        b.vy += fy;
      }
    }
  }

  for (const edge of edges) {
    const dx = edge.target.x - edge.source.x;
    const dy = edge.target.y - edge.source.y;
    const distance = Math.max(1, Math.sqrt(dx * dx + dy * dy));
    const force = (distance - springLength) * spring;
    const fx = (force * dx) / distance;
    const fy = (force * dy) / distance;
    if (!edge.source.fixed) {
      edge.source.vx += fx;
      edge.source.vy += fy;
    }
    if (!edge.target.fixed) {
      edge.target.vx -= fx;
      edge.target.vy -= fy;
    }
  }

  for (const node of nodes) {
    if (!node.fixed) {
      node.vx += (-node.x) * 0.0009;
      node.vy += (-node.y) * 0.0009;
      node.vx *= 0.86;
      node.vy *= 0.86;
      node.x += node.vx;
      node.y += node.vy;
    }
  }
}

function draw() {
  if (!ctx) return;
  ctx.clearRect(0, 0, width, height);

  ctx.save();
  ctx.translate(offsetX, offsetY);
  ctx.scale(scale, scale);

  ctx.strokeStyle = "rgba(173, 201, 224, 0.24)";
  ctx.lineWidth = 1 / Math.max(scale, 0.4);
  for (const edge of edges) {
    ctx.beginPath();
    ctx.moveTo(edge.source.x, edge.source.y);
    ctx.lineTo(edge.target.x, edge.target.y);
    ctx.stroke();
  }

  for (const node of nodes) {
    ctx.beginPath();
    ctx.fillStyle = node.color;
    ctx.arc(node.x, node.y, node.radius, 0, Math.PI * 2);
    ctx.fill();
    if (node.id === selectedID) {
      ctx.strokeStyle = "rgba(255,255,255,0.95)";
      ctx.lineWidth = 2 / Math.max(scale, 0.4);
      ctx.stroke();
    }
    ctx.font = (12 / Math.max(scale, 0.72)) + 'px ui-monospace, monospace';
    ctx.fillStyle = "rgba(230, 238, 247, 0.95)";
    ctx.fillText(node.label, node.x + node.radius + 6, node.y + 4);
  }

  ctx.restore();
}

function tick() {
  applyForces();
  draw();
  requestAnimationFrame(tick);
}

canvas.addEventListener("mousedown", (event) => {
  const point = pointerPosition(event);
  lastPointer = point;
  dragMoved = false;
  const hit = nodeAtScreen(point.x, point.y);
  if (hit) {
    dragNode = hit;
    hit.fixed = true;
    canvas.classList.add("dragging");
  } else {
    draggingCanvas = true;
    canvas.classList.add("dragging");
  }
});

window.addEventListener("mousemove", (event) => {
  if (!dragNode && !draggingCanvas) return;
  const point = pointerPosition(event);
  const dx = point.x - lastPointer.x;
  const dy = point.y - lastPointer.y;
  if (Math.abs(dx) > 2 || Math.abs(dy) > 2) dragMoved = true;
  lastPointer = point;
  if (dragNode) {
    const world = worldFromScreen(point.x, point.y);
    dragNode.x = world.x;
    dragNode.y = world.y;
    dragNode.vx = 0;
    dragNode.vy = 0;
  } else if (draggingCanvas) {
    offsetX += dx;
    offsetY += dy;
  }
});

window.addEventListener("mouseup", () => {
  if (dragNode && !dragMoved) {
    selectedID = dragNode.id;
    renderDetails(dragNode);
  }
  if (dragNode) {
    dragNode.fixed = false;
    dragNode = null;
  }
  draggingCanvas = false;
  canvas.classList.remove("dragging");
});

canvas.addEventListener("wheel", (event) => {
  event.preventDefault();
  const point = pointerPosition(event);
  const worldBefore = worldFromScreen(point.x, point.y);
  const zoom = event.deltaY < 0 ? 1.08 : 0.92;
  scale = Math.min(3.5, Math.max(0.28, scale * zoom));
  offsetX = point.x - worldBefore.x * scale;
  offsetY = point.y - worldBefore.y * scale;
}, { passive: false });

window.addEventListener("resize", resize);
resize();
renderDetails(null);
if (!ctx) {
  details.className = "";
  details.innerHTML = '<div class="empty">Canvas 2D context unavailable. YARN node list loaded, but the graph renderer could not start.</div>';
} else {
  draw();
  requestAnimationFrame(tick);
}
</script>
</body>
</html>
`
